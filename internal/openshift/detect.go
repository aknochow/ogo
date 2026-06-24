package openshift

import (
	"sync"

	"k8s.io/client-go/discovery"
)

var (
	isOpenShiftOnce   sync.Once
	isOpenShiftResult bool
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
