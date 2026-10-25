// Package opencode embeds the OpenCode plugin of this repository, so that
// `a-stack hook install --opencode` writes the plugin of the binary's own
// version.
package opencode

import _ "embed"

// Plugin is a-stack.js. Its first line names it.
//
//go:embed a-stack.js
var Plugin string

// Header is the start of the plugin's first line, also in later versions.
const Header = "// a-stack hooks for OpenCode"
