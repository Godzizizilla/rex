package config

import "strings"

// SubCommand represents a single command in a compound command pipeline.
type SubCommand struct {
	Raw      string // trimmed command text, e.g. "jq 'select(.level >= 50)'"
	Operator string // preceding operator: "" (first cmd), "|", "&&", "||", ";"
}

// Name returns the base command name (first word).
func (s SubCommand) Name() string {
	raw := strings.TrimSpace(s.Raw)
	// Skip env var assignments like VAR=val cmd
	for {
		if len(raw) == 0 {
			return ""
		}
		eqIdx := strings.Index(raw, "=")
		spIdx := strings.Index(raw, " ")
		if eqIdx > 0 && (spIdx < 0 || eqIdx < spIdx) {
			// This token is an assignment, skip it
			if spIdx < 0 {
				return raw // no space, entire thing is assignment-like
			}
			raw = strings.TrimSpace(raw[spIdx+1:])
			continue
		}
		break
	}
	if idx := strings.IndexAny(raw, " \t"); idx > 0 {
		return raw[:idx]
	}
	return raw
}

// GlobPattern returns a whitelist-friendly glob pattern: "cmdname *".
func (s SubCommand) GlobPattern() string {
	name := s.Name()
	if name == "" {
		return s.Raw
	}
	return name + " *"
}

// ParseCommand splits a compound command string into sub-commands,
// respecting single quotes, double quotes, and escape characters.
func ParseCommand(cmd string) []SubCommand {
	var result []SubCommand
	var current strings.Builder
	currentOp := ""

	i := 0
	n := len(cmd)

	for i < n {
		ch := cmd[i]

		// Handle escape
		if ch == '\\' && i+1 < n {
			current.WriteByte(ch)
			current.WriteByte(cmd[i+1])
			i += 2
			continue
		}

		// Handle single quote
		if ch == '\'' {
			current.WriteByte(ch)
			i++
			for i < n && cmd[i] != '\'' {
				current.WriteByte(cmd[i])
				i++
			}
			if i < n {
				current.WriteByte(cmd[i])
				i++
			}
			continue
		}

		// Handle double quote
		if ch == '"' {
			current.WriteByte(ch)
			i++
			for i < n && cmd[i] != '"' {
				if cmd[i] == '\\' && i+1 < n {
					current.WriteByte(cmd[i])
					current.WriteByte(cmd[i+1])
					i += 2
					continue
				}
				current.WriteByte(cmd[i])
				i++
			}
			if i < n {
				current.WriteByte(cmd[i])
				i++
			}
			continue
		}

		// Check for operators: &&, ||, |, ;
		if ch == '&' && i+1 < n && cmd[i+1] == '&' {
			raw := strings.TrimSpace(current.String())
			if raw != "" {
				result = append(result, SubCommand{Raw: raw, Operator: currentOp})
			}
			currentOp = "&&"
			current.Reset()
			i += 2
			continue
		}

		if ch == '|' && i+1 < n && cmd[i+1] == '|' {
			raw := strings.TrimSpace(current.String())
			if raw != "" {
				result = append(result, SubCommand{Raw: raw, Operator: currentOp})
			}
			currentOp = "||"
			current.Reset()
			i += 2
			continue
		}

		if ch == '|' {
			raw := strings.TrimSpace(current.String())
			if raw != "" {
				result = append(result, SubCommand{Raw: raw, Operator: currentOp})
			}
			currentOp = "|"
			current.Reset()
			i++
			continue
		}

		if ch == ';' {
			raw := strings.TrimSpace(current.String())
			if raw != "" {
				result = append(result, SubCommand{Raw: raw, Operator: currentOp})
			}
			currentOp = ";"
			current.Reset()
			i++
			continue
		}

		current.WriteByte(ch)
		i++
	}

	// Last segment
	raw := strings.TrimSpace(current.String())
	if raw != "" {
		result = append(result, SubCommand{Raw: raw, Operator: currentOp})
	}

	return result
}
