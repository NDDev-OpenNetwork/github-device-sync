// Package schemas exposes the immutable schema sources compiled into the GDS
// binary. Runtime validation never resolves schema resources over the network.
package schemas

import "embed"

// V1 contains the canonical GDS v1 JSON Schema resources.
//
//go:embed v1/*.schema.json
var V1 embed.FS
