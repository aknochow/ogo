/*
Copyright 2026 Adam Knochowski.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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

const defaultSATokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

type OpenShiftClient struct {
	oauthServerURL string
	clientID       string
	clientSecret   string
	httpClient     *http.Client
	saHTTPClient   *http.Client
	SATokenPath    string
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
		saHTTPClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		SATokenPath: defaultSATokenPath,
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
		"grant_type":   {grantAuthorizationCode},
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
	defer func() { _ = resp.Body.Close() }()

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

// GetUserInfo retrieves user info from the OpenShift users/~ API. For ServiceAccount
// identities, it additionally checks membership in the specified groupNames by querying
// Group CRs directly, since the users/~ endpoint does not return custom group memberships for SAs.
func (c *OpenShiftClient) GetUserInfo(ctx context.Context, accessToken string, groupNames ...string) (*UserInfo, error) {
	apiURL := c.kubeAPIURL()

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL+"/apis/user.openshift.io/v1/users/~", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("user info request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

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

	info := &UserInfo{
		Name:   raw.Metadata.Name,
		UID:    raw.Metadata.UID,
		Groups: raw.Groups,
	}

	if strings.HasPrefix(info.Name, "system:serviceaccount:") && len(groupNames) > 0 {
		extraGroups, err := c.checkGroupMemberships(ctx, info.Name, groupNames)
		if err != nil {
			return nil, fmt.Errorf("group CR lookup for %s: %w", info.Name, err)
		}
		seen := make(map[string]bool, len(info.Groups))
		for _, g := range info.Groups {
			seen[g] = true
		}
		for _, g := range extraGroups {
			if !seen[g] {
				info.Groups = append(info.Groups, g)
			}
		}
	}

	return info, nil
}

// checkGroupMemberships queries OpenShift Group CRs using the bridge's own SA token
// to determine which of the given groupNames contain the specified username.
// Returns the subset of groupNames the user belongs to.
func (c *OpenShiftClient) checkGroupMemberships(ctx context.Context, username string, groupNames []string) ([]string, error) {
	apiURL := c.kubeAPIURL()

	rawToken, err := os.ReadFile(c.SATokenPath)
	if err != nil {
		return nil, fmt.Errorf("reading service account token: %w", err)
	}
	saToken := strings.TrimSpace(string(rawToken))

	// groupNames is expected to be a small set (typically ≤2: UserGroup + AdminGroup),
	// so sequential lookups are fine.
	var matched []string
	for _, groupName := range groupNames {
		if groupName == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, "GET",
			apiURL+"/apis/user.openshift.io/v1/groups/"+url.PathEscape(groupName), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+saToken)

		resp, err := c.saHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("group lookup for %s: %w", groupName, err)
		}

		if resp.StatusCode == http.StatusNotFound {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
			_ = resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("group lookup for %s failed with status %d", groupName, resp.StatusCode)
		}

		var group struct {
			Users []string `json:"users"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&group)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decoding group %s: %w", groupName, err)
		}

		for _, u := range group.Users {
			if u == username {
				matched = append(matched, groupName)
				break
			}
		}
	}
	return matched, nil
}

func (c *OpenShiftClient) kubeAPIURL() string {
	apiURL := os.Getenv("KUBERNETES_API_URL")
	if apiURL == "" {
		apiURL = "https://kubernetes.default.svc:443"
	}
	return strings.TrimRight(apiURL, "/")
}
