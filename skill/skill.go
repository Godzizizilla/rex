// Package skill embeds the agent skill (SKILL.md) that teaches coding agents
// how to use rex. `rex skill install` writes it to ~/.claude/skills/rex.
package skill

import _ "embed"

//go:embed SKILL.md
var Content string
