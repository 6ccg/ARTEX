package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
)

func TestModelProfileControlsEveryCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"streaming-no-cap", Config{Stream: true, ThinkingType: "enabled", ReasoningEffort: "max"}},
		{"nonstreaming-completion-cap", Config{MaxTokens: 4096, MaxTokensField: llm.MaxTokensFieldCompletion, ThinkingType: "disabled", ReasoningEffort: "low"}},
		{"no-thinking-parameter", Config{MaxTokens: 50000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bodies := make(chan map[string]any, 4)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				bodies <- body
				if body["stream"] == true {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary\"},\"finish_reason\":null}]}\n\n")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"summary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
				}
			}))
			defer srv.Close()
			cfg := tc.cfg
			cfg.Format, cfg.BaseURL, cfg.Model, cfg.APIKey = llm.FormatOpenAI, srv.URL, "profile-model", "test-key"
			prov, err := cfg.NewProvider()
			if err != nil {
				t.Fatal(err)
			}
			temp := 0.0
			req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("hi")}, MaxTokens: 1, Thinking: "disabled", Temperature: &temp, Stop: []string{"END"}}
			msg, _, usage, err := prov.Complete(t.Context(), req)
			if err != nil || msg.Text() != "summary" || usage.OutputTokens != 2 {
				t.Fatalf("Complete: %q %+v %v", msg.Text(), usage, err)
			}
			acc := llm.NewAccumulator()
			for ev, err := range prov.Stream(t.Context(), req) {
				if err != nil {
					t.Fatal(err)
				}
				acc.Add(ev)
			}
			if acc.Message().Text() != "summary" || acc.Usage.OutputTokens != 2 {
				t.Fatalf("Stream: %q %+v", acc.Message().Text(), acc.Usage)
			}
			c := NewCompactor(prov, cfg.Model)
			if text, err := c.compress(t.Context(), nil, block{}, nil); err != nil || text != "summary" {
				t.Fatalf("cold compression: %q %v", text, err)
			}
			// Norma itself adds disabled thinking and a summary cap. The same profile
			// boundary must apply even when the override comes from the SDK.
			summarize := compaction.ProviderSummarizer(prov, 16, !cfg.Stream)
			if text, err := summarize(t.Context(), req.Messages); err != nil || text != "summary" {
				t.Fatalf("SDK compression: %q %v", text, err)
			}
			for range 4 {
				body := <-bodies
				if body["model"] != cfg.Model || body["stream"] != cfg.Stream {
					t.Fatalf("model or mode differs from profile: %+v", body)
				}
				if cfg.ThinkingType == "" {
					if _, ok := body["thinking"]; ok {
						t.Fatal("unset thinking was sent")
					}
				} else if body["thinking"].(map[string]any)["type"] != cfg.ThinkingType {
					t.Fatalf("thinking differs from profile: %v", body["thinking"])
				}
				if effort, _ := body["reasoning_effort"].(string); effort != cfg.ReasoningEffort {
					t.Fatalf("reasoning effort = %q", effort)
				}
				for _, key := range []string{"max_tokens", "max_completion_tokens"} {
					want := cfg.MaxTokens > 0 && (key == "max_completion_tokens") == (cfg.MaxTokensField == llm.MaxTokensFieldCompletion)
					v, ok := body[key]
					if ok != want || ok && v != float64(cfg.MaxTokens) {
						t.Fatalf("%s = %v, want profile cap %d (sent=%v)", key, v, cfg.MaxTokens, want)
					}
				}
				for _, key := range []string{"temperature", "stop"} {
					if _, ok := body[key]; ok {
						t.Fatalf("call-specific %s was sent", key)
					}
				}
			}
		})
	}
}

type modelProviderFixture struct {
	message llm.Message
	events  []llm.StreamEvent
	err     error
}

func (p modelProviderFixture) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.message, "stop", llm.Usage{InputTokens: 10, OutputTokens: 2}, p.err
}

func (p modelProviderFixture) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(y func(llm.StreamEvent, error) bool) {
		for _, ev := range p.events {
			if !y(ev, nil) {
				return
			}
		}
		if p.err != nil {
			y(llm.StreamEvent{}, p.err)
		}
	}
}

func TestModelProfileNonstreamingPreservesToolsAndThinking(t *testing.T) {
	msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: "reason", Signature: "signature"},
		{Type: llm.BlockToolUse, ID: "call1", Name: "tool", Input: json.RawMessage(`{"x":1}`)},
		llm.TextBlock("done"),
	}}
	p := (Config{}).ConfigureProvider(modelProviderFixture{message: msg})
	acc := llm.NewAccumulator()
	for ev, err := range p.Stream(t.Context(), llm.CompletionRequest{}) {
		if err != nil {
			t.Fatal(err)
		}
		acc.Add(ev)
	}
	if !reflect.DeepEqual(acc.Message(), msg) || acc.Usage.InputTokens != 10 || acc.StopReason != "stop" {
		t.Fatalf("converted result differs: %+v %+v", acc.Message(), acc.Usage)
	}
}

func TestModelProfileDoesNotCompletePartialStreams(t *testing.T) {
	want := errors.New("connection reset")
	for _, streamErr := range []error{nil, want} {
		p := (Config{Stream: true}).ConfigureProvider(modelProviderFixture{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "partial"}}, err: streamErr})
		msg, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{})
		if err == nil || msg.Text() != "" || streamErr != nil && !errors.Is(err, want) {
			t.Fatalf("partial stream became a completion: %q %v", msg.Text(), err)
		}
	}
}
