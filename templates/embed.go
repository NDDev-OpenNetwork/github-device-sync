// Package templates exposes canonical deterministic projection templates.
package templates

import "embed"

// Sources contains public-safe reusable templates. Repository-specific facts
// enter only through typed generator data.
//
//go:embed agents/*.tmpl github-actions/*.tmpl harnesses/*.tmpl
var Sources embed.FS
