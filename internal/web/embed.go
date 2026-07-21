package web

import "embed"

// StaticFiles embeds the generated dashboard when internal/web/dist exists.
// The package wildcard keeps clean checkout Go commands compiling before the
// web build has produced ignored dist assets.
//
//go:embed all:*
var StaticFiles embed.FS
