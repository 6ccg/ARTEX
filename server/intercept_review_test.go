package server

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/llm"
)

func TestInterceptRulesThenJudge(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	m := newTestManager(t)
	i := m.interceptor
	saved := i.GetJudgeConfig()
	t.Cleanup(func() { _ = i.SetJudgeConfig(saved) })
	cfg := saved
	cfg.Enabled, cfg.ProfileID, cfg.FailAction = true, 1<<62, "allow"
	if err := i.SetJudgeConfig(cfg); err != nil {
		t.Fatal(err)
	}
	(&Runtime{m: m}).wireInterceptReviewer()
	g := guard.NewWithInterceptor(i)
	input := json.RawMessage(`{"command":"rm -rf /tmp/test-owned-by-task"}`)
	if blocked, message, _ := g.Hooks().PreToolUse(t.Context(), "Bash", input); !blocked || !strings.Contains(message, "审批未完成，未执行") {
		t.Fatalf("missing judge profile must deny as incomplete: blocked=%v message=%s", blocked, message)
	}
	for _, tc := range []struct {
		command, verdict string
		enabled, blocked bool
		calls            int
	}{
		{"ls -la", "deny", false, false, 0},
		{"python scan.py", "allow", false, true, 0},
		{"ls -la", "deny", true, false, 0},
		{"rm -rf /", "allow", true, true, 0},
		{"rm -rf /tmp/test-owned-by-task", "allow", true, false, 1},
		{"rm -rf /tmp/test-owned-by-task", "deny", true, true, 1},
	} {
		cfg.Enabled = tc.enabled
		if err := i.SetJudgeConfig(cfg); err != nil {
			t.Fatal(err)
		}
		calls := 0
		i.SetReviewer(func(context.Context, int64, string, intercept.ReviewInput) (intercept.Decision, error) {
			calls++
			return intercept.Decision{Action: tc.verdict}, nil
		})
		args, _ := json.Marshal(map[string]string{"command": tc.command})
		blocked, _, _ := g.Hooks().PreToolUse(t.Context(), "Bash", args)
		if blocked != tc.blocked || calls != tc.calls {
			t.Fatalf("command=%q enabled=%v verdict=%s blocked=%v calls=%d", tc.command, tc.enabled, tc.verdict, blocked, calls)
		}
	}
}

func TestInterceptExplainsIncompleteAndDeniedApprovals(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	m := newTestManager(t)
	i := m.interceptor
	saved := i.GetJudgeConfig()
	t.Cleanup(func() { _ = i.SetJudgeConfig(saved) })
	cfg := saved
	cfg.Enabled, cfg.FailAction, cfg.RetryCount = true, "deny", 0
	cfg.AskTimeoutSeconds, cfg.AskTimeoutAction = 1, "deny"
	if err := i.SetJudgeConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, verdict, want string
		failed, manual      bool
	}{
		{"model_error", "", "审批未完成，未执行", true, false},
		{"model_deny", "deny", "实际操作：删除订单 8821", false, false},
		{"manual_deny", "ask", "人工明确拒绝", false, true},
		{"approval_timeout", "ask", "审批未完成，未执行：人工审批等待超时", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i.SetReviewer(func(context.Context, int64, string, intercept.ReviewInput) (intercept.Decision, error) {
				if tc.failed {
					return intercept.Decision{}, errors.New("model request failed")
				}
				return intercept.Decision{Action: tc.verdict, Message: "实际操作：删除订单 8821；成功后的后果：订单记录丢失；命中规则：D4"}, nil
			})
			var pendingID int64
			ctx := intercept.WithTaskContext(t.Context(), "", "", func(a db.Activity) {
				var detail struct {
					PendingID int64 `json:"pending_id"`
				}
				if err := json.Unmarshal([]byte(a.Detail), &detail); err != nil {
					t.Fatal(err)
				}
				pendingID = detail.PendingID
				if tc.manual {
					if err := i.Decide(pendingID, false); err != nil {
						t.Fatal(err)
					}
				}
			})
			g := guard.NewWithInterceptor(i)
			blocked, message, _ := g.Hooks().PreToolUse(ctx, "Bash", json.RawMessage(`{"command":"python scan.py"}`))
			if !blocked || !strings.Contains(message, tc.want) || !strings.Contains(message, "其他可行路线") || strings.Contains(message, "此操作被禁止") {
				t.Fatalf("blocked=%v message=%s", blocked, message)
			}
			if !tc.failed && (tc.verdict == "deny" || tc.manual) && strings.Contains(message, "审批未完成") {
				t.Fatalf("explicit denial misreported as incomplete: %s", message)
			}
			if pendingID != 0 {
				detail, err := m.pg.GetInterceptDetail(pendingID)
				if err != nil || detail == nil || detail.Audit == nil || detail.Audit.ExecutionStatus != "not_executed" || (tc.manual && detail.DecisionSource != "manual") || (!tc.manual && detail.Status != "timeout") {
					t.Fatalf("incorrect saved approval: %+v error=%v", detail, err)
				}
			}
		})
	}
}

type reviewCaptureProvider struct{ request llm.CompletionRequest }

func (p *reviewCaptureProvider) Stream(_ context.Context, request llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.request = request
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{Type: llm.SETextDelta, Text: `{"decision":"ask","comment":"实际操作：删除文件；成功后的后果：文件会丢失，归属尚未确认；命中规则：ASK（归属不明）"}`}, nil)
	}
}
func (p *reviewCaptureProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

// Assert the actual model request, rather than merely the envelope helper.
func TestReviewCompletionSendsCurrentCallWithoutHistory(t *testing.T) {
	ctx := intercept.WithReviewContext(t.Context(), "/tmp/review-fixture", intercept.ReviewBackground{Source: intercept.BackgroundUserMessage, Text: "清理本次测试文件"})
	ctx, trace := intercept.WithTrace(ctx, "GLOBAL_OVERVIEW_SENTINEL", []db.InterceptContextEntry{
		{Kind: "tool_use", Tool: "Write", ToolUseID: "created", Text: `{"path":"probe.txt"}`},
		{Kind: "tool_result", ToolUseID: "created", Text: "file created"},
		{Kind: "assistant", Text: "untrusted worker speculation"},
	})
	args := json.RawMessage(`{"command":"rm probe.txt","timeout":1000}`)
	trace.Start("current", "Bash", args)
	input, err := intercept.BuildReviewInput(intercept.WithCall(ctx, "Bash", args), "Bash", args)
	if err != nil {
		t.Fatal(err)
	}
	p := &reviewCaptureProvider{}
	profile := agent.Config{Stream: true, MaxTokens: 3072}
	prompt := intercept.EffectiveJudgePrompt(intercept.DefaultJudgePrompt)
	reply, err := reviewCompletion(ctx, bindSideProvider(p, profile, 1, "review"), prompt, input)
	if err != nil || intercept.ParseVerdict(reply).Action != "ask" {
		t.Fatalf("%s %v", reply, err)
	}
	if len(p.request.System) != 1 || p.request.System[0] != prompt || len(p.request.Messages) != 1 {
		t.Fatal("wrong review request roles")
	}
	if !strings.Contains(prompt, intercept.JudgeOutputContract) || p.request.MaxTokens != profile.MaxTokens || p.request.Thinking != "" || p.request.Temperature != nil {
		t.Fatal("review request differs from the configured model profile")
	}
	body := p.request.Messages[0].Content[0].Text
	var got intercept.ReviewInput
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 6 {
		t.Fatalf("unexpected envelope keys: %s", body)
	}
	for _, key := range []string{"version", "working_directory", "background", "script_capture_status", "tool_name", "arguments"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing %s", key)
		}
	}
	if strings.Contains(body, "file created") || strings.Contains(body, `"path"`) {
		t.Fatal("history leaked into actual model request")
	}
	if got.Version != 5 || got.Background == nil || got.Background.Source != intercept.BackgroundUserMessage || got.Background.Text != "清理本次测试文件" || got.WorkingDir != "/tmp/review-fixture" || string(got.Arguments) != string(args) || strings.Contains(body, "worker speculation") || strings.Contains(body, "GLOBAL_OVERVIEW_SENTINEL") {
		t.Fatalf("wrong model input: %s", body)
	}
}
