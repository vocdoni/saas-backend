// Package root uses the embed package to include static files.
package root

import "embed"

// Assets is a virtual filesystem containing all files embedded from the assets directory.
//
//go:embed all:assets
var Assets embed.FS
