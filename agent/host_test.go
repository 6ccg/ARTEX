package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

func TestChatInstancesKeepTheirOwnHost(t *testing.T) {
	dir := t.TempDir()
	var calls [2]int
	providers := []*autoTitleProvider{{reply: "done"}, {reply: "done"}}
	agents := make([]*ChatAgent, 2)
	for i, name := range []string{"instance A", "instance B"} {
		host := &Host{
			PromptOverride: func(string) (string, bool) { return name, true },
			ToolResolve: func(_ context.Context, _ string, tools []actool.CoreTool) []actool.CoreTool {
				calls[i]++
				return tools
			},
		}
		agents[i] = NewChatAgent(host, providers[i], "fixture", dir, nil, 0)
		agents[i].SetNonStreaming(func() bool { return true })
	}
	// Build both instances before running either, then use A again after B.
	for _, i := range []int{0, 1, 0} {
		if _, err := agents[i].Chat(t.Context(), "fixture", []string{"a", "b"}[i], "hello", 2, time.Second, false, nil); err != nil {
			t.Fatal(err)
		}
		want := []string{"instance A", "instance B"}[i]
		if len(providers[i].req.System) == 0 || !strings.HasPrefix(providers[i].req.System[0], want) {
			t.Fatalf("instance %d used another host's prompt: %v", i, providers[i].req.System)
		}
	}
	if calls != [2]int{2, 1} {
		t.Fatalf("tool resolution crossed instance boundaries: %v", calls)
	}
}
