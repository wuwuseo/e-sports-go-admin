//go:build tools

// Package tools pins code-generation dependencies that are not part of the
// application build, so `go mod tidy` keeps them in go.mod.
package tools

import _ "github.com/wuwuseo/ent/extension/tableprefix"
