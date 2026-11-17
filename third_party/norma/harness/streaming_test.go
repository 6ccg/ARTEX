package harness

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

func TestStreamErrorRecordsExecutedToolsAndDropsPartialCall(t *testing.T) {
	done := make(chan struct{})
	var calls atomic.Int32
	probe := tool.Build(tool.Spec{
		Name: "probe", Schema: map[string]any{"type": "object"},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
			calls.Add(1)
			close(done)
			return tool.Text("committed"), nil
		},
	})
	wantErr := errors.New("stream disconnected")
	model := func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		return func(yield func(llm.StreamEvent, error) bool) {
			for _, ev := range []llm.StreamEvent{
				{Type: llm.SEToolUseStart, ToolID: "complete", ToolName: "probe"},
				{Type: llm.SEToolInputJSON, Text: `{}`},
				{Type: llm.SETextDelta, Text: "running"},
			} {
				if !yield(ev, nil) {
					return
				}
			}
			<-done
			yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "partial", ToolName: "probe"}, nil)
			yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: `{"incomplete":`}, nil)
			yield(llm.StreamEvent{}, wantErr)
		}
	}
	rec := &capRecorder{}
	term := drain(t, QueryInput{Messages: []llm.Message{llm.UserText("go")}, Tools: tool.NewRegistry(probe),
		PermissionMode: permission.ModeBypass, Recorder: rec}, QueryDeps{CallModel: model})
	if !errors.Is(term.Err, wantErr) || calls.Load() != 1 || len(term.Messages) != 3 || len(rec.msgs) != 2 {
		t.Fatalf("terminal=%+v calls=%d recorded=%d", term, calls.Load(), len(rec.msgs))
	}
	uses := term.Messages[1].ToolUses()
	results := term.Messages[2].Content
	if len(uses) != 1 || uses[0].ID != "complete" || len(results) != 1 || results[0].ToolUseID != "complete" || results[0].Content[0].Text != "committed" {
		t.Fatalf("incomplete recovery history: %+v", term.Messages)
	}
}

func TestQueryWaitsForToolCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, released := make(chan struct{}), make(chan struct{})
	toolDone, queryDone := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	probe := tool.Build(tool.Spec{
		Name: "cleanup_probe", Schema: map[string]any{"type": "object"},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			defer close(toolDone)
			close(started)
			<-ctx.Done()
			<-released
			return tool.Text("cleanup completed"), nil
		},
	})
	model := &scriptedModel{turns: [][]llm.StreamEvent{toolTurn("probe", probe.Name(), `{}`)}}
	go func() {
		defer close(queryDone)
		for range Query(ctx, QueryInput{
			Messages: []llm.Message{llm.UserText("go")}, Tools: tool.NewRegistry(probe), PermissionMode: permission.ModeBypass,
		}, QueryDeps{CallModel: model.call}) {
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	cancel()
	select {
	case <-queryDone:
		t.Fatal("query returned before tool cleanup")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-queryDone:
	case <-time.After(time.Second):
		t.Fatal("query did not finish after cleanup")
	}
	select {
	case <-toolDone:
	default:
		t.Fatal("query returned before tool cleanup")
	}
}

// TestStreamingToolExecutionOverlapsStream proves FR-01.4: a read-only,
// concurrency-safe tool begins executing as soon as its tool_use block finishes
// streaming, while the rest of the assistant message is still being produced.
//
// The scripted model emits tool A, then tool B (whose start finalizes A and
// dispatches it), then BLOCKS — refusing to finish the message until A has
// actually run. If tools only ran after the stream completed, this would
// deadlock; the test's timeout would then fire.
func TestStreamingToolExecutionOverlapsStream(t *testing.T) {
	ran := make(chan string, 4)
	probe := tool.Build(tool.Spec{
		Name: "probe",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
		},
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(_ context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var v struct {
				ID string `json:"id"`
			}
			json.Unmarshal(in, &v)
			ran <- v.ID
			return tool.Text("ok"), nil
		},
	})

	var turn int32
	callModel := func(_ context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		n := atomic.AddInt32(&turn, 1)
		return func(yield func(llm.StreamEvent, error) bool) {
			if n > 1 { // second turn: finish.
				yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "done"}, nil)
				yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn"}, nil)
				yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
				return
			}
			yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "a", ToolName: "probe"}, nil)
			yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: `{"id":"a"}`}, nil)
			// Starting B finalizes A, so A is dispatched and runs concurrently.
			yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "b", ToolName: "probe"}, nil)
			yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: `{"id":"b"}`}, nil)
			// Refuse to finish the message until A has actually executed.
			for v := range ran {
				if v == "a" {
					break
				}
			}
			yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "tool_use"}, nil)
			yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
		}
	}

	in := QueryInput{
		Messages:       []llm.Message{llm.UserText("go")},
		Tools:          tool.NewRegistry(probe),
		PermissionMode: permission.ModeBypass,
	}

	done := make(chan *Terminal, 1)
	go func() {
		var term *Terminal
		for ev, err := range Query(context.Background(), in, QueryDeps{CallModel: callModel}) {
			if err != nil {
				t.Errorf("event err: %v", err)
				break
			}
			if ev.Kind == KindResult {
				term = ev.Terminal
			}
		}
		done <- term
	}()

	select {
	case term := <-done:
		if term == nil || term.Reason != ReasonCompleted {
			t.Fatalf("terminal=%+v", term)
		}
		// Both probes produced tool_results, paired in order.
		results := term.Messages[2].Content
		if len(results) != 2 || results[0].ToolUseID != "a" || results[1].ToolUseID != "b" {
			t.Fatalf("results=%+v", results)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: tool did not execute during streaming (FR-01.4 regression)")
	}
}
