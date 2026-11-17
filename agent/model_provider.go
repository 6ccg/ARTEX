package agent

import (
	"context"
	"errors"
	"iter"

	"github.com/Autumn-27/norma/llm"
)

// ConfigureProvider makes the selected profile authoritative for every call,
// including SDK summaries and helpers that choose a different Provider method.
func (c Config) ConfigureProvider(inner llm.Provider) llm.Provider {
	return &modelProvider{inner: inner, maxTokens: max(c.MaxTokens, 0), streaming: c.Stream}
}

type modelProvider struct {
	inner     llm.Provider
	maxTokens int
	streaming bool
}

func (p *modelProvider) request(req llm.CompletionRequest) llm.CompletionRequest {
	req.MaxTokens = p.maxTokens
	req.Thinking = "" // the SDK uses the profile's ThinkingType and ReasoningEffort
	req.Temperature = nil
	req.Stop = nil
	return req
}

func (p *modelProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	req = p.request(req)
	if !p.streaming {
		return p.inner.Complete(ctx, req)
	}
	acc := llm.NewAccumulator()
	complete := false
	for ev, err := range p.inner.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", acc.Usage, err
		}
		acc.Add(ev)
		complete = complete || ev.Type == llm.SEMessageStop
	}
	if err := ctx.Err(); err != nil {
		return llm.Message{}, "", acc.Usage, err
	}
	if !complete {
		return llm.Message{}, "", acc.Usage, errors.New("model stream ended without completion")
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}

func (p *modelProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	req = p.request(req)
	if p.streaming {
		return p.inner.Stream(ctx, req)
	}
	return func(yield func(llm.StreamEvent, error) bool) {
		msg, stop, usage, err := p.inner.Complete(ctx, req)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		if err := ctx.Err(); err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: usage}, nil) {
			return
		}
		for _, block := range msg.Content {
			var event llm.StreamEvent
			switch block.Type {
			case llm.BlockText:
				event = llm.StreamEvent{Type: llm.SETextDelta, Text: block.Text}
			case llm.BlockThinking:
				if !yield(llm.StreamEvent{Type: llm.SEThinkingDelta, Text: block.Thinking}, nil) {
					return
				}
				event = llm.StreamEvent{Type: llm.SEThinkingSignature, Text: block.Signature}
			case llm.BlockToolUse:
				if !yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: block.ID, ToolName: block.Name}, nil) {
					return
				}
				event = llm.StreamEvent{Type: llm.SEToolInputJSON, Text: string(block.Input)}
			default:
				continue
			}
			if !yield(event, nil) {
				return
			}
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: stop}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}
