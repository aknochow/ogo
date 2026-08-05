/*
Copyright 2026.

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

package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// oldestByCreationTimestamp returns the item with the earliest
// CreationTimestamp, generalizing the oldest-wins tie-break used to enforce
// singleton semantics across this package: OpenShellGatewayReconciler's
// cluster-wide singleton enforcement, resolveGateway's singleton gateway
// lookup, and OpenShellPolicyReconciler's active-global-policy selection all
// need the exact same "which of these same-kind objects came first" answer.
// T must be a pointer to a type embedding metav1.ObjectMeta (satisfied by
// every generated CRD type in api/v1alpha1) since GetCreationTimestamp has a
// pointer receiver.
//
// Precondition: items must be non-empty -- every current call site already
// guards this (each only calls in after confirming len(items) > 0), so this
// panics with a clear message instead of silently returning a zero value
// that could mask a caller's missing guard.
func oldestByCreationTimestamp[T metav1.Object](items []T) T {
	if len(items) == 0 {
		panic("oldestByCreationTimestamp: items must be non-empty")
	}
	oldest := items[0]
	oldestTS := oldest.GetCreationTimestamp()
	for _, item := range items[1:] {
		ts := item.GetCreationTimestamp()
		if ts.Before(&oldestTS) {
			oldest = item
			oldestTS = ts
		}
	}
	return oldest
}
