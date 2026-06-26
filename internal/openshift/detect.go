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
