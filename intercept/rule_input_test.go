package intercept

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestMatchExecutionInsteadOfMentionedKeywords(t *testing.T) {
	i := &Interceptor{cached: []compiledRule{{
		InterceptRule: db.InterceptRule{Name: "[内置] 危险命令", MatchTarget: "tool_input", MatchType: "regex", Action: "deny"},
		re:            regexp.MustCompile(`rm.*-rf|DROP TABLE|requests.delete|reboot`),
	}}}
	for _, tc := range []struct {
		tool, command, action string
	}{
		{"Bash", "ls -la && cat README.md", "allow"},
		{"shell_open", "pwd", "allow"},
		{"Bash", `printf '%s' 'DROP TABLE demo; rm -rf /'`, "allow"},
		{"Bash", "rm -rf /tmp/my-test", ""},
		{"Bash", "rm -rf /", "deny"},
		{"Bash", "rm -rf / --help", ""},
		{"Bash", "reboot", "deny"},
		{"Bash", "reboot --help", ""},
		{"Bash", "echo ok > /etc/example", ""},
		{"Bash", "echo $(rm -rf /)", ""},
		{"Bash", "cat <(rm -rf /)", ""},
		{"Bash", "ls; rm -rf /", ""},
		{"Bash", "printf -v PATH /tmp", ""},
		{"Bash", "python -c 'requests.delete(url)'", ""},
		{"Write", "Report: rm -rf /; DROP TABLE demo", ""},
		{"WebFetch", "https://target/delete", ""},
	} {
		t.Run(tc.command, func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{"command": tc.command, "content": tc.command, "url": tc.command})
			got, matched := i.Match(tc.tool, input)
			if matched != (tc.action != "") || got.Action != tc.action {
				t.Fatalf("decision=%+v matched=%v want=%q", got, matched, tc.action)
			}
		})
	}
	// Explicit administrator rules still take precedence over automatic approvals.
	i.cached = append(i.cached, compiledRule{InterceptRule: db.InterceptRule{
		Name: "自定义限制", MatchTarget: "tool_input", MatchType: "string", Pattern: "ls", Action: "deny",
	}})
	if got, _ := i.Match("Bash", []byte(`{"command":"ls"}`)); got.Action != "deny" {
		t.Fatal("custom rule was bypassed")
	}
}
