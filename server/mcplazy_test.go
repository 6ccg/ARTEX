package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestLazyMCPConnectionLifecycle(t *testing.T) {
	var initCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			initCount.Add(1)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"test","version":"1"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"ping","inputSchema":{"type":"object"}}]}}`, req.ID)
		case "tools/call":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"ok"}]}}`, req.ID)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	l := newLazyMCP(&db.MCPServer{Name: "test", Transport: "http", URL: srv.URL})
	tool := l.tool("mcp__test__ping", "", map[string]any{"type": "object"})
	_ = tool.InputSchema()
	if initCount.Load() != 0 {
		t.Fatal("schema access initialized MCP")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := tool.Call(context.Background(), json.RawMessage(`{}`), nil)
			if err != nil || res.IsError {
				t.Errorf("call: %v %s", err, res.Flatten())
			}
		}()
	}
	wg.Wait()
	if initCount.Load() != 1 {
		t.Fatalf("initialize count = %d", initCount.Load())
	}
	_ = l.Close()
	res, _ := tool.Call(context.Background(), json.RawMessage(`{}`), nil)
	if !res.IsError || initCount.Load() != 1 {
		t.Fatal("closed client reconnected")
	}
}

func TestLazyMCPCloseCancelsInitialization(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req any
		_ = json.NewDecoder(r.Body).Decode(&req)
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	l := newLazyMCP(&db.MCPServer{Name: "test", Transport: "http", URL: srv.URL})
	tool := l.tool("mcp__test__ping", "", map[string]any{"type": "object"})
	done := make(chan struct{})
	go func() { defer close(done); tool.Call(context.Background(), json.RawMessage(`{}`), nil) }()
	<-started
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestLazyMCPCloseCancelsInFlightCall(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"test","version":"1"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"ping","inputSchema":{"type":"object"}}]}}`, req.ID)
		case "tools/call":
			close(started)
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	l := newLazyMCP(&db.MCPServer{Name: "test", Transport: "http", URL: srv.URL})
	tool := l.tool("mcp__test__ping", "", map[string]any{"type": "object"})
	done := make(chan struct{})
	go func() { defer close(done); tool.Call(context.Background(), json.RawMessage(`{}`), nil) }()
	<-started
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
}
