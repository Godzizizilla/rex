// Package policy decides which remote operations may run without a human.
//
// Commands are parsed with a real shell parser (mvdan.cc/sh) instead of being
// split on operators, so constructs such as newlines, "&", "$(...)", backticks
// and redirections can no longer smuggle a second command past a whitelist rule
// like "tail *".
package policy

import (
	"fmt"
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Command is one simple command found in a shell command line.
type Command struct {
	Raw    string `json:"raw"`              // source text, e.g. "tail -n 5 app.log"
	Name   string `json:"name"`             // command name, e.g. "tail" ("" if dynamic)
	Op     string `json:"op,omitempty"`     // operator before it: "", "|", "&&", "||", ";"
	Nested bool   `json:"nested,omitempty"` // inside $(...), bash -c, if/for, ...

	// Filled in by Evaluate.
	Whitelisted bool   `json:"whitelisted"`
	Rule        string `json:"rule,omitempty"`
	Risk        string `json:"risk,omitempty"` // blocklist pattern that matched, if any

	effective string // wrappers such as sudo/env/xargs stripped
}

// Analysis is the structural breakdown of a shell command line.
type Analysis struct {
	Commands []Command `json:"commands"`
	// Constructs that make the command line ineligible for whitelist
	// auto-approval (e.g. "command substitution", "redirect to /etc/hosts").
	Constructs []string `json:"constructs,omitempty"`
	// Reasons the command cannot be fully analyzed (parse error, dynamic
	// command name, eval of a variable, ...). Never auto-approved.
	Opaque []string `json:"opaque,omitempty"`
	// Redirect targets that write outside scratch locations.
	RiskyWrites []string `json:"risky_writes,omitempty"`
}

// Analyze parses a shell command line. stdin is the data that will be fed to
// the remote command, if any; when the command is a shell reading its script
// from stdin, that script is analyzed as well.
func Analyze(command string, stdin []byte) *Analysis {
	a := &analyzer{res: &Analysis{}, stdin: stdin}
	a.parse(command, "", false, 0)
	return a.res
}

type analyzer struct {
	res   *Analysis
	stdin []byte
}

const maxDepth = 4

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "ash": true, "fish": true}

// wrappers run their arguments as a command. The value lists the options
// that consume the following argument.
var wrappers = map[string]string{
	"sudo": "-u -g -p -C -h -r -t -U -D", "doas": "-u -C", "env": "-u -C -S",
	"nohup": "", "nice": "-n", "ionice": "-c -n -p", "time": "-o -f",
	"timeout": "-s -k", "stdbuf": "-i -o -e", "command": "", "exec": "-a",
	"xargs": "-I -L -n -P -d -E -s -a", "watch": "-n -d", "setsid": "", "strace": "-e -o -p -s",
}

func (a *analyzer) parse(src, op string, nested bool, depth int) {
	if depth > maxDepth {
		a.opaque("too deeply nested")
		return
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		a.opaque("parse error: " + err.Error())
		return
	}
	w := &walker{a: a, src: src, depth: depth}
	for i, s := range file.Stmts {
		o := op
		if i > 0 {
			o = ";"
		}
		w.stmt(s, o, nested)
	}
}

func (a *analyzer) construct(s string) {
	for _, c := range a.res.Constructs {
		if c == s {
			return
		}
	}
	a.res.Constructs = append(a.res.Constructs, s)
}

func (a *analyzer) opaque(s string) {
	a.res.Opaque = append(a.res.Opaque, s)
}

// walker walks one parsed source string (the top level, a bash -c argument, ...).
type walker struct {
	a     *analyzer
	src   string
	depth int
}

func (w *walker) text(n syntax.Node) string {
	start, end := n.Pos().Offset(), n.End().Offset()
	if int(end) > len(w.src) || start > end {
		return ""
	}
	return w.src[start:end]
}

func (w *walker) stmt(s *syntax.Stmt, op string, nested bool) {
	if s.Background {
		w.a.construct("background job (&)")
	}
	if s.Coprocess {
		w.a.construct("coprocess")
	}
	var heredoc string
	for _, r := range s.Redirs {
		w.redirect(r)
		if r.Hdoc != nil {
			if lit, ok := literal(r.Hdoc); ok {
				heredoc = lit
			} else {
				heredoc = w.text(r.Hdoc)
			}
		}
		if r.Op == syntax.WordHdoc && r.Word != nil {
			heredoc, _ = literal(r.Word)
		}
	}
	if s.Cmd == nil {
		return
	}
	switch c := s.Cmd.(type) {
	case *syntax.CallExpr:
		w.call(c, op, nested, heredoc)
	case *syntax.BinaryCmd:
		w.stmt(c.X, op, nested)
		w.stmt(c.Y, c.Op.String(), nested)
	case *syntax.Subshell:
		w.stmts(c.Stmts, op, nested)
	case *syntax.Block:
		w.stmts(c.Stmts, op, nested)
	default:
		w.a.construct(constructName(c))
		w.nested(c)
	}
}

func (w *walker) stmts(list []*syntax.Stmt, op string, nested bool) {
	for i, s := range list {
		o := op
		if i > 0 {
			o = ";"
		}
		w.stmt(s, o, nested)
	}
}

// nested walks an arbitrary node, analyzing every statement found inside it.
func (w *walker) nested(n syntax.Node) {
	syntax.Walk(n, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.Stmt:
			w.stmt(x, "", true)
			return false
		case *syntax.CmdSubst:
			w.a.construct("command substitution")
		case *syntax.ProcSubst:
			w.a.construct("process substitution")
		}
		return true
	})
}

func (w *walker) redirect(r *syntax.Redirect) {
	if r.Word != nil {
		w.nested(r.Word)
	}
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll,
		syntax.AppAll, syntax.RdrInOut:
	default:
		return // input redirects, dups like 2>&1, heredocs
	}
	target, ok := literal(r.Word)
	if !ok {
		w.a.construct("redirect to dynamic path")
		w.a.res.RiskyWrites = append(w.a.res.RiskyWrites, w.text(r.Word))
		return
	}
	switch target {
	case "/dev/null", "/dev/stdout", "/dev/stderr":
		return
	}
	w.a.construct("writes file " + target)
	if strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "/tmp/") && !strings.HasPrefix(target, "/var/tmp/") {
		w.a.res.RiskyWrites = append(w.a.res.RiskyWrites, target)
	}
}

func (w *walker) call(c *syntax.CallExpr, op string, nested bool, heredoc string) {
	for _, as := range c.Assigns {
		w.nested(as)
	}
	for _, word := range c.Args {
		w.nested(word)
	}
	if len(c.Args) == 0 {
		return // plain assignment
	}

	parts := make([]string, 0, len(c.Assigns)+len(c.Args))
	for _, as := range c.Assigns {
		parts = append(parts, w.text(as))
	}
	args := make([]string, len(c.Args))
	for i, word := range c.Args {
		parts = append(parts, w.text(word))
		if lit, ok := literal(word); ok {
			args[i] = lit
		} else {
			args[i] = w.text(word)
		}
	}
	cmd := Command{Raw: strings.Join(parts, " "), Op: op, Nested: nested}
	name, ok := literal(c.Args[0])
	if !ok {
		w.a.opaque("dynamic command name: " + w.text(c.Args[0]))
	} else {
		cmd.Name = name
	}

	eff := stripWrappers(args)
	cmd.effective = strings.Join(eff, " ")
	w.a.res.Commands = append(w.a.res.Commands, cmd)
	if len(eff) == 0 {
		return
	}

	base := path.Base(eff[0])
	switch {
	case base == "eval":
		w.a.construct("eval")
		lits, ok := literalArgs(c.Args[len(args)-len(eff)+1:])
		if !ok {
			w.a.opaque("eval of dynamic string")
			return
		}
		w.a.parse(strings.Join(lits, " "), "", true, w.depth+1)
	case base == "source" || base == ".":
		w.a.construct("source")
	case shells[base]:
		w.shell(c, eff, heredoc)
	}
}

// shell analyzes "bash -c '...'" and scripts fed to a shell over stdin.
func (w *walker) shell(c *syntax.CallExpr, eff []string, heredoc string) {
	offset := len(c.Args) - len(eff)
	for i := 1; i < len(eff); i++ {
		arg := eff[i]
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			if arg != "--" {
				w.a.construct("runs script file " + arg)
				return
			}
			break
		}
		if !strings.HasPrefix(arg, "--") && strings.Contains(arg, "c") {
			w.a.construct("shell -c")
			if i+1 >= len(eff) {
				w.a.opaque("shell -c without script")
				return
			}
			script, ok := literal(c.Args[offset+i+1])
			if !ok {
				w.a.opaque("shell -c with dynamic script")
				return
			}
			w.a.parse(script, "", true, w.depth+1)
			return
		}
	}
	w.a.construct("shell script from stdin")
	switch {
	case heredoc != "":
		w.a.parse(heredoc, "", true, w.depth+1)
	case len(w.a.stdin) > 0:
		w.a.parse(string(w.a.stdin), "", true, w.depth+1)
	default:
		w.a.opaque("shell reads an unknown script from stdin")
	}
}

// stripWrappers removes leading command wrappers (sudo, env, xargs, ...) and
// their options so the effective command can be checked against the blocklist.
func stripWrappers(args []string) []string {
	for len(args) > 0 {
		base := path.Base(args[0])
		valueOpts, ok := wrappers[base]
		if !ok {
			return args
		}
		args = args[1:]
		for len(args) > 0 {
			a := args[0]
			switch {
			case a == "--":
				args = args[1:]
				goto next
			case strings.HasPrefix(a, "-"):
				args = args[1:]
				if len(args) > 0 && strings.Contains(" "+valueOpts+" ", " "+a+" ") {
					args = args[1:]
				}
			case base == "env" && strings.Contains(a, "="):
				args = args[1:]
			case (base == "timeout" || base == "watch") && isDuration(a):
				args = args[1:]
			default:
				goto next
			}
		}
	next:
	}
	return args
}

func isDuration(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if (r < '0' || r > '9') && r != '.' && !(i == len(s)-1 && strings.ContainsRune("smhd", r)) {
			return false
		}
	}
	return true
}

// literal returns the unquoted value of a word without expansions.
func literal(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	var sb strings.Builder
	if !literalParts(&sb, w.Parts) {
		return "", false
	}
	return sb.String(), true
}

func literalParts(sb *strings.Builder, parts []syntax.WordPart) bool {
	for _, p := range parts {
		switch x := p.(type) {
		case *syntax.Lit:
			sb.WriteString(unescape(x.Value))
		case *syntax.SglQuoted:
			sb.WriteString(x.Value)
		case *syntax.DblQuoted:
			if !literalParts(sb, x.Parts) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

func literalArgs(words []*syntax.Word) ([]string, bool) {
	out := make([]string, len(words))
	for i, w := range words {
		lit, ok := literal(w)
		if !ok {
			return nil, false
		}
		out[i] = lit
	}
	return out, true
}

func constructName(c syntax.Command) string {
	switch c.(type) {
	case *syntax.IfClause:
		return "if statement"
	case *syntax.WhileClause:
		return "while loop"
	case *syntax.ForClause:
		return "for loop"
	case *syntax.CaseClause:
		return "case statement"
	case *syntax.FuncDecl:
		return "function definition"
	case *syntax.DeclClause:
		return "declaration (export/local/declare)"
	case *syntax.TestClause:
		return "test expression [[ ]]"
	case *syntax.ArithmCmd, *syntax.LetClause:
		return "arithmetic"
	case *syntax.TimeClause:
		return "time"
	case *syntax.CoprocClause:
		return "coprocess"
	}
	return fmt.Sprintf("%T", c)
}
