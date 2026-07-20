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

package envoygateway

import "embed"

// Envoy Gateway v1.3.2 install manifests, split into three parts
// to allow selective installation (e.g. skip Gateway API CRDs on OCP 4.20+).
//
// Update these files when upgrading Envoy Gateway:
//   curl -sL https://github.com/envoyproxy/gateway/releases/download/v1.3.2/install.yaml -o /tmp/eg.yaml
//   sed -n '1,14978p' /tmp/eg.yaml > gatewayapi-crds.yaml
//   sed -n '14979,39991p' /tmp/eg.yaml > envoygateway-crds.yaml
//   sed -n '39992,$p' /tmp/eg.yaml > components.yaml

const Version = "v1.3.2"

//go:embed gatewayapi-crds.yaml envoygateway-crds.yaml components.yaml
var Manifests embed.FS
