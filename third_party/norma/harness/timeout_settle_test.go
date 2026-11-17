package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

func TestSettlementUsesLiveContextAndRejectsDisabledTool(t *testing.T) {
	calls := 0
	probe := tool.Build(tool.Spec{
		Name: "probe", Schema: map[string]any{"type": "object"},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			calls++
			if calls == 2 {
				select {
				case <-time.After(120 * time.Millisecond):
				case <-ctx.Done():
					return tool.Errorf("settlement cancelled"), ctx.Err()
				}
			}
			return tool.Text("saved"), nil
		},
	})
	m := &scriptedModel{turns: [][]llm.StreamEvent{
		toolTurn("first", "probe", `{}`), toolTurn("save", "probe", `{}`), toolTurn("denied", "forbidden", `{}`),
	}}
	forbiddenCalls := 0
	forbidden := tool.Build(tool.Spec{Name: "forbidden", Schema: map[string]any{"type": "object"},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
			forbiddenCalls++
			return tool.Text("unexpected"), nil
		},
	})
	term := drain(t, QueryInput{Messages: []llm.Message{llm.UserText("go")}, Tools: tool.NewRegistry(probe, forbidden),
		PermissionMode: permission.ModeBypass, MaxTurns: 1, MaxDuration: 80 * time.Millisecond,
		Settlement: &Settlement{Prompt: "save facts", MaxTurns: 2, DisabledTools: []string{"forbidden"}}}, QueryDeps{CallModel: m.call})
	if calls != 2 || forbiddenCalls != 0 || m.calls != 3 || term.Reason != ReasonMaxTurns {
		t.Fatalf("calls=%d forbidden=%d turns=%d terminal=%+v", calls, forbiddenCalls, m.calls, term)
	}
	if result := term.Messages[5].Content[0]; result.IsError || result.Content[0].Text != "saved" {
		t.Fatalf("settlement write failed: %+v", result)
	}
}

// drainWithCtx is drain() over a caller-supplied ctx, so a test can cancel the
// parent (pause/kill/shutdown) independently of the run's own MaxDuration budget.
func drainWithCtx(t *testing.T, ctx context.Context, in QueryInput, deps QueryDeps) *Terminal {
	t.Helper()
	var term *Terminal
	for ev, err := range Query(ctx, in, deps) {
		if err != nil {
			t.Fatalf("event err: %v", err)
		}
		if ev.Kind == KindResult {
			term = ev.Terminal
		}
	}
	if term == nil {
		t.Fatal("no terminal event")
	}
	return term
}

// When a tool overruns the MaxDuration wall-clock budget, the tool is interrupted
// at the deadline and the run enters the WRAP-UP phase (settlement) on the live
// ctx — finishing with ReasonTimeout and the wrap-up prompt injected — instead of
// dying with ReasonAbortedTools. This is the hung/slow-tool case that the old
// turn-boundary-only budget check could never settle.
func TestSettlementOnToolOverrunsBudget(t *testing.T) {
	m := &scriptedModel{turns: [][]llm.StreamEvent{
		toolTurn("s1", "sleep", `{"seconds":3600}`), // hangs far past MaxDuration
		textTurn("已写回事实并总结"),                        // settlement turn → natural stop
	}}
	in := QueryInput{
		Messages:       []llm.Message{llm.UserText("go")},
		Tools:          tool.NewRegistry(tool.NewSleep()),
		PermissionMode: permission.ModeBypass,
		WorkingDir:     t.TempDir(),
		MaxDuration:    100 * time.Millisecond, // budget the run so the sleep overruns it
		Settlement:     &Settlement{Prompt: "SETTLE_ON_TIMEOUT 请写回事实"},
	}
	term := drainWithCtx(t, context.Background(), in, QueryDeps{CallModel: m.call})

	if term.Reason != ReasonTimeout {
		t.Fatalf("reason=%v, want ReasonTimeout（工具超墙→就地收尾，非 aborted_tools）", term.Reason)
	}
	found := false
	for _, msg := range term.Messages {
		for _, c := range msg.Content {
			if strings.Contains(c.Text, "SETTLE_ON_TIMEOUT") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("超墙收尾提示未被注入到对话")
	}
	if m.calls != 2 {
		t.Fatalf("应有 2 次模型调用(主+收尾)，得 %d", m.calls)
	}
}

// Regression guard: a PARENT-ctx cancellation (pause / planner kill / shutdown) —
// as opposed to the run's own budget — still aborts with ReasonAbortedTools and
// does NOT enter the wrap-up phase.
func TestParentCancelStillAbortsNoSettlement(t *testing.T) {
	m := &scriptedModel{turns: [][]llm.StreamEvent{
		toolTurn("s1", "sleep", `{"seconds":3600}`),
		textTurn("would-be settlement"), // must NOT be reached
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	in := QueryInput{
		Messages:       []llm.Message{llm.UserText("go")},
		Tools:          tool.NewRegistry(tool.NewSleep()),
		PermissionMode: permission.ModeBypass,
		WorkingDir:     t.TempDir(),
		// no MaxDuration: the only deadline is the parent ctx above
		Settlement: &Settlement{Prompt: "SHOULD_NOT_APPEAR"},
	}
	term := drainWithCtx(t, ctx, in, QueryDeps{CallModel: m.call})

	if term.Reason != ReasonAbortedTools {
		t.Fatalf("reason=%v, want ReasonAbortedTools（父 ctx 取消=真中断，不收尾）", term.Reason)
	}
	for _, msg := range term.Messages {
		for _, c := range msg.Content {
			if strings.Contains(c.Text, "SHOULD_NOT_APPEAR") {
				t.Fatalf("父 ctx 取消时不应进入收尾，却注入了收尾提示")
			}
		}
	}
	if m.calls != 1 {
		t.Fatalf("父取消后不应再调用模型收尾，模型调用数=%d，want 1", m.calls)
	}
}
