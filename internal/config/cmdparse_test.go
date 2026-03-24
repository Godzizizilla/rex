package config

import (
	"testing"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		input    string
		expected []SubCommand
	}{
		{
			input: "tail -n 100 /var/log/app.log",
			expected: []SubCommand{
				{Raw: "tail -n 100 /var/log/app.log", Operator: ""},
			},
		},
		{
			input: "journalctl CONTAINER_TAG=app -o cat | jq 'select(.level >= 50)'",
			expected: []SubCommand{
				{Raw: "journalctl CONTAINER_TAG=app -o cat", Operator: ""},
				{Raw: "jq 'select(.level >= 50)'", Operator: "|"},
			},
		},
		{
			input: "echo 'hello | world' | grep hello",
			expected: []SubCommand{
				{Raw: "echo 'hello | world'", Operator: ""},
				{Raw: "grep hello", Operator: "|"},
			},
		},
		{
			input: `grep "a && b" file | wc -l`,
			expected: []SubCommand{
				{Raw: `grep "a && b" file`, Operator: ""},
				{Raw: "wc -l", Operator: "|"},
			},
		},
		{
			input: "cmd1 && cmd2 || cmd3 ; cmd4",
			expected: []SubCommand{
				{Raw: "cmd1", Operator: ""},
				{Raw: "cmd2", Operator: "&&"},
				{Raw: "cmd3", Operator: "||"},
				{Raw: "cmd4", Operator: ";"},
			},
		},
		{
			input: "journalctl --since '1 day ago' -o cat | grep 'token expired' | jq .",
			expected: []SubCommand{
				{Raw: "journalctl --since '1 day ago' -o cat", Operator: ""},
				{Raw: "grep 'token expired'", Operator: "|"},
				{Raw: "jq .", Operator: "|"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ParseCommand(tt.input)
			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d subcommands, got %d: %+v", len(tt.expected), len(result), result)
			}
			for i, sc := range result {
				if sc.Raw != tt.expected[i].Raw {
					t.Errorf("[%d] raw: expected %q, got %q", i, tt.expected[i].Raw, sc.Raw)
				}
				if sc.Operator != tt.expected[i].Operator {
					t.Errorf("[%d] operator: expected %q, got %q", i, tt.expected[i].Operator, sc.Operator)
				}
			}
		})
	}
}

func TestSubCommandName(t *testing.T) {
	tests := []struct {
		raw      string
		expected string
	}{
		{"tail -n 100 /var/log/app.log", "tail"},
		{"jq 'select(.level >= 50)'", "jq"},
		{"grep hello", "grep"},
		{"VAR=val cmd arg", "cmd"},
		{"wc -l", "wc"},
	}
	for _, tt := range tests {
		sc := SubCommand{Raw: tt.raw}
		if got := sc.Name(); got != tt.expected {
			t.Errorf("Name(%q) = %q, want %q", tt.raw, got, tt.expected)
		}
	}
}

func TestSubCommandGlobPattern(t *testing.T) {
	sc := SubCommand{Raw: "jq 'select(.level >= 50)'"}
	if got := sc.GlobPattern(); got != "jq *" {
		t.Errorf("GlobPattern() = %q, want %q", got, "jq *")
	}
}
