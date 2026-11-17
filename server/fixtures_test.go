package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	if _, _, err := db.DSN(); err != nil {
		t.Skipf("no database config: %v", err)
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// API fixtures exercise handlers without starting real scans or notifications.
func newIdleAPIServer(t *testing.T, m *Manager) *Server {
	t.Helper()
	dir := t.TempDir()
	runtime, err := NewRuntime(context.Background(), m, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	s, err := NewHandler(runtime, dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func authenticatedRequest(t *testing.T, s *Server) func(string, string, string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	return func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
}

// Most integration fixtures intentionally exercise the running backend.
func newStartedServer(t *testing.T, ctx context.Context, m *Manager, skillDir, dataDir, keyDir string) *Server {
	t.Helper()
	runtime, err := NewRuntime(ctx, m, skillDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	s, err := NewHandler(runtime, keyDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	return s
}
