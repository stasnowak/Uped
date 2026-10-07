// Package web embeds the browser UI (plain HTML, CSS and JavaScript with no
// build step) into the uped binary.
package web

import "embed"

// FS holds the web assets. index.html sits at the root.
// Add new asset files to the go:embed line below.
//
//go:embed index.html
var FS embed.FS
