package lib

import "embed"

// embeddedFS contains the built-in library task trees.
// Each subdirectory under builtin/ is a library module.
//
//go:embed all:builtin
var embeddedFS embed.FS
