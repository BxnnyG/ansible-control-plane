// Package api embeds the OpenAPI document of the REST API.
package api

import _ "embed"

// Spec is the OpenAPI 3.1 document served at GET /api/v1/openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte
