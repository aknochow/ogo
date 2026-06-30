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

	"k8s.io/client-go/discovery"
)

var (
	isOpenShiftOnce   sync.Once
	isOpenShiftResult bool

	hasGatewayAPIOnce   sync.Once
	hasGatewayAPIResult bool
)

// IsOpenShift checks whether the cluster has the route.openshift.io API group.
// The result is cached for the lifetime of the process.
func IsOpenShift(dc discovery.DiscoveryInterface) bool {
	isOpenShiftOnce.Do(func() {
		_, err := dc.ServerResourcesForGroupVersion("route.openshift.io/v1")
		isOpenShiftResult = err == nil
	})
	return isOpenShiftResult
}

// HasGatewayAPI checks whether the cluster has the gateway.networking.k8s.io API group.
// The result is cached for the lifetime of the process.
func HasGatewayAPI(dc discovery.DiscoveryInterface) bool {
	hasGatewayAPIOnce.Do(func() {
		_, err := dc.ServerResourcesForGroupVersion("gateway.networking.k8s.io/v1")
		hasGatewayAPIResult = err == nil
	})
	return hasGatewayAPIResult
}
