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

package openshift

import (
	"sync"
	"time"

	"k8s.io/client-go/discovery"
)

const (
	positiveTTL = 5 * time.Minute
	negativeTTL = 30 * time.Second
)

type cachedDetection struct {
	mu     sync.Mutex
	result bool
	expiry time.Time
}

func (c *cachedDetection) check(dc discovery.DiscoveryInterface, groupVersion string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expiry) {
		return c.result
	}
	_, err := dc.ServerResourcesForGroupVersion(groupVersion)
	c.result = err == nil
	if c.result {
		c.expiry = time.Now().Add(positiveTTL)
	} else {
		c.expiry = time.Now().Add(negativeTTL)
	}
	return c.result
}

var (
	isOpenShiftCache   cachedDetection
	hasGatewayAPICache cachedDetection
	hasGroupsAPICache  cachedDetection
)

// IsOpenShift checks whether the cluster has the route.openshift.io API group.
// Result is cached with TTL: 5 min positive, 30s negative.
func IsOpenShift(dc discovery.DiscoveryInterface) bool {
	return isOpenShiftCache.check(dc, "route.openshift.io/v1")
}

// HasGatewayAPI checks whether the cluster has the gateway.networking.k8s.io API group.
// Result is cached with TTL: 5 min positive, 30s negative.
func HasGatewayAPI(dc discovery.DiscoveryInterface) bool {
	return hasGatewayAPICache.check(dc, "gateway.networking.k8s.io/v1")
}

// HasGroupsAPI checks whether the cluster has the user.openshift.io groups API.
// Result is cached with TTL: 5 min positive, 30s negative.
func HasGroupsAPI(dc discovery.DiscoveryInterface) bool {
	return hasGroupsAPICache.check(dc, "user.openshift.io/v1")
}
