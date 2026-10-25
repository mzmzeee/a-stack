// Package systemd embeds the systemd user unit of this repository, so that
// `a-stack service install` writes the unit of the binary's own version.
package systemd

import _ "embed"

// Unit is a-stack.service.
//
//go:embed a-stack.service
var Unit string
