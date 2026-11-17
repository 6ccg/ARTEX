package server

import (
	"context"
	"net"
	"testing"
)

func TestManagerStartupFailureReleasesProxyListener(t *testing.T) {
	m := newTestManager(t)
	previous, exists, err := m.pg.GetSetting(settingLLMProxy)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if exists {
			m.pg.SetSetting(settingLLMProxy, previous)
		} else {
			m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingLLMProxy)
		}
	}()
	if err := m.pg.SetSetting(settingLLMProxy, "invalid://proxy"); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if failed, err := NewManager(t.TempDir(), addr); err == nil {
		failed.Close()
		t.Fatal("invalid proxy setting did not fail startup")
	}
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("failed constructor retained proxy listener: %v", err)
	}
	ln.Close()
}

func TestRuntimeRequiresExplicitStart(t *testing.T) {
	m := newTestManager(t)
	task, err := m.CreateTask("explicit startup", "remain idle before Start", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	if err := m.ApplyTaskPause(task.ID); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := NewRuntime(context.Background(), m, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	defer s.Close()
	if s.engine.Started(task.ID) {
		t.Fatal("constructor started task loops")
	}
	handler, err := NewHandler(s, dir)
	if err != nil {
		t.Fatal(err)
	}
	if handler.Runtime != s || s.engine.Started(task.ID) {
		t.Fatal("HTTP adapter changed runtime ownership or started task loops")
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	run := s.engine.lookupTaskRun(task.ID)
	if run == nil || !run.started.Load() {
		t.Fatal("explicit Start did not restore task loops")
	}
	if err := s.Start(); err != nil || s.engine.lookupTaskRun(task.ID) != run {
		t.Fatalf("repeated Start replaced the task owner: %v", err)
	}
	s.Close()
	if err := s.Start(); err == nil {
		t.Fatal("closed runtime restarted background producers")
	}
	if err := m.pg.PingContext(context.Background()); err != nil {
		t.Fatalf("runtime closed application-owned storage: %v", err)
	}
}
