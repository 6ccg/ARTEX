package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

type activityStreamWriter struct {
	header          http.Header
	body            strings.Builder
	writes, flushes int
	fail            bool
	cancel          context.CancelFunc
	onFlush         func(int)
}

func (w *activityStreamWriter) Header() http.Header { return w.header }
func (w *activityStreamWriter) WriteHeader(int)     {}
func (w *activityStreamWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.fail {
		return 0, io.ErrClosedPipe
	}
	return w.body.Write(p)
}
func (w *activityStreamWriter) Flush() {
	w.flushes++
	if w.onFlush != nil {
		w.onFlush(w.flushes)
	} else {
		w.cancel()
	}
}

func TestLiveActivityReadsCommittedRowsAfterCoalescedReverseNotifications(t *testing.T) {
	m := newTestManager(t)
	task, err := m.CreateTask("stream burst", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	engine := NewEngine(m)
	s := &Server{Runtime: &Runtime{m: m, engine: engine}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	writer := &activityStreamWriter{header: http.Header{}, cancel: cancel}
	var ids []int64
	writer.onFlush = func(flush int) {
		if flush != 1 {
			cancel()
			return
		}
		rows, err := m.pg.Query(`INSERT INTO activity(exploration_id,worker,kind) SELECT $1,'system','text' FROM generate_series(1,300) RETURNING id`, task.ExpID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		rows.Close()
		for i := len(ids) - 1; i >= 0; i-- {
			engine.Broadcaster().Publish(task.ID, db.Activity{ID: ids[i]})
		}
	}
	s.streamActivity(writer, httptest.NewRequest(http.MethodGet, "/api/exploration/activity/stream?task="+task.ID, nil).WithContext(ctx))
	if len(ids) != 300 || strings.Count(writer.body.String(), "\ndata: ") != 300 {
		t.Fatalf("burst lost rows: persisted=%d delivered=%d", len(ids), strings.Count(writer.body.String(), "\ndata: "))
	}
	previous := -1
	for _, id := range ids {
		position := strings.Index(writer.body.String(), fmt.Sprintf("id: %d\n", id))
		if position <= previous {
			t.Fatalf("out-of-order/missing id %d", id)
		}
		previous = position
	}
}

func TestActivityReplayBatchesAndStopsOnDisconnect(t *testing.T) {
	m := newTestManager(t)
	task, err := m.CreateTask("stream replay", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	var ids []int64
	for range 3 {
		id, err := task.Store.AppendActivity(db.Activity{Worker: "system", Kind: "text", Summary: "replay"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	s := &Server{Runtime: &Runtime{m: m, engine: NewEngine(m)}}
	for _, name := range []string{"cancelled", "write_error", "replay", "resume"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			request := httptest.NewRequest(http.MethodGet, "/api/activity/stream?task="+task.ID, nil).WithContext(ctx)
			if name == "cancelled" {
				cancel()
			}
			if name == "resume" {
				request.Header.Set("Last-Event-ID", strconv.FormatInt(ids[0], 10))
			}
			writer := &activityStreamWriter{header: http.Header{}, fail: name == "write_error", cancel: cancel}
			s.streamActivity(writer, request)
			switch name {
			case "cancelled":
				if writer.writes != 0 || writer.flushes != 0 {
					t.Fatalf("cancelled replay wrote %d, flushed %d", writer.writes, writer.flushes)
				}
			case "write_error":
				if writer.writes != 1 || writer.flushes != 0 {
					t.Fatalf("failed replay kept writing: writes=%d flushes=%d", writer.writes, writer.flushes)
				}
			default:
				want := ids
				if name == "resume" {
					want = ids[1:]
				}
				if writer.writes != len(want) || writer.flushes != 1 {
					t.Fatalf("replay writes=%d flushes=%d", writer.writes, writer.flushes)
				}
				for _, id := range want {
					if !strings.Contains(writer.body.String(), fmt.Sprintf("id: %d\ndata:", id)) {
						t.Fatalf("missing SSE cursor %d", id)
					}
				}
			}
		})
	}
}
