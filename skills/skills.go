// Package skills embeds the agent skills of this repository, so that
// `a-stack skill install` writes the skill of the binary's own version.
package skills

import "embed"

// FS holds the skill folders, such as a-stack/SKILL.md.
//
//go:embed a-stack
var FS embed.FS
