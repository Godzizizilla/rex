package policy

import (
	"strings"

	"rex/internal/config"
)

// DefaultBlocklist lists command patterns that temporary auto-approve mode
// never approves on its own. Patterns are case-insensitive globs matched
// against each simple command (with and without wrappers such as sudo/xargs).
// Patterns that start with "*" are content patterns and are also matched
// against data sent over stdin (e.g. SQL piped into psql).
var DefaultBlocklist = []string{
	// files & disks
	"rm *", "rmdir *", "shred *", "truncate *", "dd *", "mkfs*", "fdisk *", "parted *", "wipefs *",
	"find *-delete*", "find *-exec rm*", "chmod *", "chown *", "chattr *",
	// power & processes
	"shutdown*", "reboot*", "poweroff*", "halt*", "init *", "telinit *",
	"kill *", "killall *", "pkill *",
	// services
	"systemctl stop *", "systemctl restart *", "systemctl disable *", "systemctl mask *",
	"systemctl kill *", "systemctl reboot*", "systemctl poweroff*", "service * stop", "service * restart",
	// network & users
	"iptables *", "ip6tables *", "nft *", "ufw *", "firewall-cmd *", "ip link *", "ip route *",
	"userdel *", "useradd *", "usermod *", "passwd*", "crontab *",
	// containers
	"docker rm *", "docker rmi *", "docker stop *", "docker kill *", "docker system prune*",
	"docker volume rm *", "docker volume prune*", "docker compose down*", "docker-compose down*",
	"kubectl delete *", "kubectl drain *", "kubectl scale *", "helm uninstall *", "helm delete *",
	// vcs
	"git push*--force*", "git push* -f*", "git reset --hard*", "git clean *",
	// data
	"*drop table*", "*drop database*", "*drop schema*", "*truncate table*", "*delete from*",
	"*flushall*", "*flushdb*", "*dropdatabase*",
}

// Evaluation is the policy verdict for one request.
type Evaluation struct {
	Kind     string    `json:"kind"`               // exec, upload, download, forward
	Target   string    `json:"target,omitempty"`   // remote path or host:port for non-exec kinds
	Commands []Command `json:"commands,omitempty"` // exec only

	Constructs  []string `json:"constructs,omitempty"`
	Opaque      []string `json:"opaque,omitempty"`
	RiskyWrites []string `json:"risky_writes,omitempty"`

	// Whitelisted means the configured whitelist covers the whole request.
	Whitelisted bool   `json:"whitelisted"`
	Rule        string `json:"rule,omitempty"` // note(s) of the matched rules
	// Risks explains why auto-approve mode must not approve this request.
	Risks []string `json:"risks,omitempty"`
}

// Blocklist returns the effective auto-approve blocklist.
func Blocklist(cfg *config.Config) []string {
	var list []string
	if !cfg.AutoApprove.DisableDefaultBlocklist {
		list = append(list, DefaultBlocklist...)
	}
	return append(list, cfg.AutoApprove.Blocklist...)
}

// EvaluateExec evaluates a shell command for the given server.
func EvaluateExec(cfg *config.Config, server, command string, stdin []byte) *Evaluation {
	an := Analyze(command, stdin)
	ev := &Evaluation{
		Kind:        config.RuleExec,
		Commands:    an.Commands,
		Constructs:  an.Constructs,
		Opaque:      an.Opaque,
		RiskyWrites: an.RiskyWrites,
	}

	block := Blocklist(cfg)
	allListed := len(ev.Commands) > 0
	var notes []string
	for i := range ev.Commands {
		c := &ev.Commands[i]
		if rule := matchRule(cfg, config.RuleExec, server, c.Raw); rule != nil {
			c.Whitelisted = true
			c.Rule = rule.Note
			if rule.Note != "" {
				notes = append(notes, rule.Note)
			}
		} else {
			allListed = false
		}
		if p := matchBlock(block, c.Raw, c.effective); p != "" {
			c.Risk = p
			ev.Risks = append(ev.Risks, "high-risk command: "+c.Raw+" (matches "+p+")")
		}
	}
	if len(stdin) > 0 {
		lower := strings.ToLower(string(stdin))
		for _, p := range block {
			if strings.HasPrefix(p, "*") && globMatch(strings.ToLower(p), lower) {
				ev.Risks = append(ev.Risks, "stdin matches "+p)
			}
		}
	}
	for _, w := range an.RiskyWrites {
		ev.Risks = append(ev.Risks, "writes "+w)
	}
	for _, o := range an.Opaque {
		ev.Risks = append(ev.Risks, "cannot analyze: "+o)
	}

	ev.Whitelisted = allListed && len(an.Constructs) == 0 && len(an.Opaque) == 0
	if ev.Whitelisted {
		ev.Rule = strings.Join(dedupe(notes), "; ")
	}
	return ev
}

// EvaluateTarget evaluates an upload, download or forward request whose
// whitelist rules match a single target (a remote path or host:port).
func EvaluateTarget(cfg *config.Config, kind, server, target string) *Evaluation {
	ev := &Evaluation{Kind: kind, Target: target}
	if rule := matchRule(cfg, kind, server, target); rule != nil {
		ev.Whitelisted = true
		ev.Rule = rule.Note
	}
	if kind == config.RuleUpload {
		for _, p := range []string{"/etc/", "/usr/", "/bin/", "/sbin/", "/boot/", "/lib", "/root/", "/.ssh/", "authorized_keys"} {
			if strings.Contains(target, p) {
				ev.Risks = append(ev.Risks, "uploads into "+p)
				break
			}
		}
	}
	return ev
}

func matchRule(cfg *config.Config, kind, server, s string) *config.WhitelistRule {
	for i := range cfg.Whitelist {
		r := &cfg.Whitelist[i]
		// "name *" also covers the bare command "name".
		if r.Kind() == kind && r.AppliesTo(server) && (globMatch(r.Pattern, s) || globMatch(r.Pattern, s+" ")) {
			return r
		}
	}
	return nil
}

func matchBlock(block []string, raw, effective string) string {
	raw, effective = strings.ToLower(raw), strings.ToLower(effective)
	for _, p := range block {
		lp := strings.ToLower(p)
		// "rm *" should also catch a bare "rm" (e.g. "xargs rm").
		if globMatch(lp, raw) || globMatch(lp, raw+" ") ||
			(effective != "" && (globMatch(lp, effective) || globMatch(lp, effective+" "))) {
			return p
		}
	}
	return ""
}

// GlobPattern suggests a whitelist pattern for a command: "name *".
func GlobPattern(c Command) string {
	if c.Name == "" {
		return c.Raw
	}
	return c.Name + " *"
}

// globMatch matches a pattern against a string where * matches any characters.
func globMatch(pattern, str string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == str
	}
	if !strings.HasPrefix(str, parts[0]) {
		return false
	}
	str = str[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(str, parts[i])
		if idx < 0 {
			return false
		}
		str = str[idx+len(parts[i]):]
	}
	return strings.HasSuffix(str, parts[len(parts)-1])
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
