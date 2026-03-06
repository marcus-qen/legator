// Package docs embeds the Legator OpenAPI specification for use in the control plane.
package docs

import _ "embed"

// OpenAPISpec is the embedded OpenAPI 3.1 specification for the Legator control-plane REST API.
//
//go:embed openapi.yaml
var OpenAPISpec []byte
