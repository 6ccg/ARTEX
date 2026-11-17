package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

type lazyMCP struct {
	ctx          context.Context
	cancel       context.CancelFunc
	server       *db.MCPServer
	mu           sync.Mutex
	client       mcpClient
	tools        map[string]actool.CoreTool
	initializing chan struct{}
	closed       bool
	active       sync.WaitGroup
}

// The connection belongs to the run cleanup, not the preparation context,
// which may expire before later tool calls (including settlement).
func newLazyMCP(m *db.MCPServer) *lazyMCP {
	ctx, cancel := context.WithCancel(context.Background())
	return &lazyMCP{ctx: ctx, cancel: cancel, server: m}
}

func (l *lazyMCP) get(ctx context.Context, name string) (actool.CoreTool, error) {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, fmt.Errorf("MCP server %s is closed", l.server.Name)
		}
		if l.client != nil {
			t := l.tools[name]
			l.mu.Unlock()
			if t == nil {
				return nil, fmt.Errorf("MCP server %s no longer exposes tool %s; refresh discovery", l.server.Name, name)
			}
			return t, nil
		}
		if l.initializing != nil {
			ch := l.initializing
			l.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-l.ctx.Done():
				return nil, l.ctx.Err()
			}
		}
		ch := make(chan struct{})
		l.initializing = ch
		l.mu.Unlock()
		initCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		stop := context.AfterFunc(l.ctx, cancel)
		started := time.Now()
		cl, err := connectMCP(initCtx, l.server)
		var ts []actool.CoreTool
		if err == nil {
			ts, err = cl.Tools(initCtx)
		}
		stop()
		cancel()
		l.mu.Lock()
		if err == nil && !l.closed {
			l.client = cl
			l.tools = make(map[string]actool.CoreTool, len(ts))
			for _, t := range ts {
				l.tools[t.Name()] = t
			}
		} else if cl != nil {
			_ = cl.Close()
		}
		l.initializing = nil
		close(ch)
		closed := l.closed
		l.mu.Unlock()
		if closed {
			return nil, fmt.Errorf("MCP server %s is closed", l.server.Name)
		}
		if err != nil {
			return nil, fmt.Errorf("MCP server %s initialization failed: %w", l.server.Name, err)
		}
		log.Printf("[mcp] %s 按需连接完成，初始化耗时 %s", l.server.Name, time.Since(started).Round(time.Millisecond))
	}
}
func (l *lazyMCP) tool(name, description string, schema map[string]any) actool.CoreTool {
	return actool.Build(actool.Spec{Name: name, Description: description, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.AskUser("call MCP tool " + name + "?")
		},
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			l.mu.Lock()
			if l.closed {
				l.mu.Unlock()
				return actool.Errorf("MCP server is closed"), nil
			}
			l.active.Add(1)
			l.mu.Unlock()
			defer l.active.Done()
			callCtx, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(l.ctx, cancel)
			defer func() { stop(); cancel() }()
			t, err := l.get(callCtx, name)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return t.Call(callCtx, in, tc)
		},
	})
}
func (l *lazyMCP) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.active.Wait()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.client != nil {
		return l.client.Close()
	}
	return nil
}

func failedDiscoveryTool(name, server string, err error) actool.CoreTool {
	message := fmt.Sprintf("MCP server %s discovery failed: %v", server, err)
	return actool.Build(actool.Spec{Name: name, Description: message, Schema: map[string]any{"type": "object", "description": message}, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		return actool.Errorf(message), nil
	}})
}
