package server

import (
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

func TestLogSinkDetachDrainsAcceptedWrites(t *testing.T) {
	m := newTestManager(t)
	sink := &logSinkT{cap: 20, subs: map[chan LogLine]struct{}{}, out: io.Discard}
	tag := fmt.Sprintf("log-drain-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM server_logs WHERE tag=$1`, tag) })
	stop := sink.attachDB(m.pg)
	t.Cleanup(stop)
	for i := range 10 {
		sink.add(LogLine{Level: "info", Tag: tag, Text: fmt.Sprint(i)})
	}
	stop()
	stop()
	sink.add(LogLine{Level: "info", Tag: tag, Text: "after detach"})
	var count int
	if err := m.pg.QueryRow(`SELECT count(*) FROM server_logs WHERE tag=$1`, tag).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 10 {
		t.Fatalf("persisted %d accepted log writes, want 10", count)
	}

	stopNext := sink.attachDB(m.pg)
	t.Cleanup(stopNext)
	sink.add(LogLine{Level: "info", Tag: tag, Text: "next runtime"})
	stopNext()
	if err := m.pg.QueryRow(`SELECT count(*) FROM server_logs WHERE tag=$1`, tag).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 11 {
		t.Fatalf("next runtime persisted %d total writes, want 11", count)
	}
}

func TestLogSinkConcurrentUnsubscribe(t *testing.T) {
	sink := &logSinkT{cap: 10, subs: map[chan LogLine]struct{}{}, out: io.Discard}
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 2000 {
			sink.add(LogLine{Text: "concurrent log"})
		}
	})
	for range 2000 {
		_, unsubscribe := sink.subscribe()
		unsubscribe()
	}
	wg.Wait()
}
