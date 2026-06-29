package authbridge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type OpenShiftClient struct {
	oauthServerURL string
	clientID       string
	clientSecret   string
	httpClient     *http.Client
}

func NewOpenShiftClient(oauthServerURL, clientID, clientSecret string) *OpenShiftClient {
	transport := &http.Transport{}
	caCert, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err == nil {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(caCert)
		transport.TLSClientConfig = &tls.Config{RootCAs: pool}
	}

	return &OpenShiftClient{
		oauthServerURL: strings.TrimRight(oauthServerURL, "/"),
		clientID:       clientID,
		clientSecret:   clientSecret,
		httpClient:     &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

func (c *OpenShiftClient) AuthorizationURL() string {
	return c.oauthServerURL + "/oauth/authorize"
}

func (c *OpenShiftClient) TokenURL() string {
	return c.oauthServerURL + "/oauth/token"
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

func (c *OpenShiftClient) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.TokenURL(), strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.clientID, c.clientSecret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decoding token response: %w", err)
	}
	return &tokenResp, nil
}

type UserInfo struct {
	Name   string   `json:"name"`
	UID    string   `json:"uid"`
	Groups []string `json:"groups"`
}

func (c *OpenShiftClient) GetUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	apiURL := os.Getenv("KUBERNETES_API_URL")
	if apiURL == "" {
		apiURL = "https://kubernetes.default.svc:443"
	}
	apiURL = strings.TrimRight(apiURL, "/")

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL+"/apis/user.openshift.io/v1/users/~", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("user info request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("user info failed (%d): %s", resp.StatusCode, string(body))
	}

	var raw struct {
		Metadata struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"metadata"`
		Groups []string `json:"groups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding user info: %w", err)
	}

	return &UserInfo{
		Name:   raw.Metadata.Name,
		UID:    raw.Metadata.UID,
		Groups: raw.Groups,
	}, nil
}
