package intercept

import (
	"encoding/json"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ruleInput recognizes a small set of literal operations. Everything else keeps
// its complete original input for model review; no command is executed here.
func ruleInput(tool string, input []byte) (string, bool) {
	if tool != "Bash" && tool != "shell_open" {
		return "", false
	}
	var args struct{ Command string }
	if json.Unmarshal(input, &args) != nil || strings.TrimSpace(args.Command) == "" || len(args.Command) > maxReviewScriptBytes {
		return "", false
	}
	tree, err := syntax.NewParser().Parse(strings.NewReader(args.Command), "rule")
	if err != nil {
		return "", false
	}
	// Only a single direct invocation can be a deterministic destructive match.
	// Wrappers, conditionals, scripts and substitutions require model review.
	if len(tree.Stmts) == 1 && !tree.Stmts[0].Background && len(tree.Stmts[0].Redirs) == 0 {
		if call, ok := tree.Stmts[0].Cmd.(*syntax.CallExpr); ok {
			if words, known := literalCall(call); known && criticalCall(words) {
				return strings.Join(words, " "), false
			}
		}
	}
	readOnly := len(tree.Stmts) > 0
	syntax.Walk(tree, func(node syntax.Node) bool {
		switch n := node.(type) {
		case nil:
			return readOnly
		case *syntax.File, *syntax.Word, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted, *syntax.Comment:
		case *syntax.Stmt:
			if n.Background || len(n.Redirs) != 0 {
				readOnly = false
			}
		case *syntax.BinaryCmd:
			// All branches still have to consist entirely of read-only calls.
		case *syntax.CallExpr:
			words, known := literalCall(n)
			if !known || !readOnlyCall(words) {
				readOnly = false
			}
		default:
			readOnly = false
		}
		return readOnly
	})
	return "", readOnly
}

func literalCall(call *syntax.CallExpr) ([]string, bool) {
	if len(call.Assigns) != 0 || len(call.Args) == 0 {
		return nil, false
	}
	words := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		word, ok := staticWord(arg)
		if !ok {
			return nil, false
		}
		words = append(words, word)
	}
	return words, true
}

func readOnlyCall(words []string) bool {
	switch words[0] {
	case "echo", "cat", "ls", "pwd", "whoami", "id", "ps", "head", "tail", "wc":
		return true
	case "printf":
		return len(words) == 1 || words[1] != "-v"
	case "uname", "hostname":
		return len(words) == 1
	}
	return false
}

func criticalCall(words []string) bool {
	for _, word := range words[1:] {
		if word == "--help" || word == "--version" {
			return false
		}
	}
	switch words[0] {
	case "shutdown", "reboot", "halt", "poweroff":
		return true
	case "rm":
		for _, word := range words[1:] {
			if word == "/" {
				return true
			}
		}
	case "mkfs", "mkfs.ext4", "mkfs.xfs", "dd", "shred", "wipe":
		for _, word := range words[1:] {
			path := strings.TrimPrefix(word, "of=")
			if strings.HasPrefix(path, "/dev/sd") || strings.HasPrefix(path, "/dev/vd") || strings.HasPrefix(path, "/dev/nvme") {
				return true
			}
		}
	}
	return false
}
