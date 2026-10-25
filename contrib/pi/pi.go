// Package pi embeds the Pi extension of this repository, so that
// `a-stack hook install --pi` writes the extension of the binary's own version.
package pi

import _ "embed"

// Extension is a-stack.ts. Its first line names it.
//
//go:embed a-stack.ts
var Extension string

// Header is the start of the extension's first line, also in later versions.
const Header = "// a-stack hooks for Pi"
