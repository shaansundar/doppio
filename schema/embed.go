// Package schema embeds the published JSON Schemas for doppio outputs.
package schema

import "embed"

// FS holds scp/v1/manifest.schema.json and scp/v1/scene.schema.json.
//
//go:embed scp/v1/*.json
var FS embed.FS
