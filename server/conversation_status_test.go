package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestConversationListRunningState(t *testing.T) {
	s, _ := newRetestServer(t)
	var ids []int64
	for _, key := range []string{"auto", "reporter", "retester"} {
		c, err := s.m.pg.CreateConversation(key, "runtime status test", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	t.Cleanup(func() {
		s.chatMu.Lock()
		clear(s.chatBusy)
		s.chatMu.Unlock()
		for _, id := range ids {
			_, _ = s.m.pg.Exec(`DELETE FROM conversations WHERE id=$1`, id)
		}
	})
	check := func(want map[int64]bool) {
		t.Helper()
		w := retestRequest(s.pgListConversations, http.MethodGet, 0, "")
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
		var body struct {
			Conversations []conversationListItem `json:"conversations"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, item := range body.Conversations {
			if expected, ok := want[item.ID]; ok {
				found++
				if item.Running != expected || item.Title != "runtime status test" {
					t.Fatalf("conversation %d: running=%v want=%v title=%q", item.ID, item.Running, expected, item.Title)
				}
			}
		}
		if found != len(want) {
			t.Fatalf("found %d of %d conversations", found, len(want))
		}
	}
	check(map[int64]bool{ids[0]: false, ids[1]: false, ids[2]: false})
	s.chatMu.Lock()
	s.chatBusy[s.convBusyKey(ids[1])] = true
	s.chatBusy[s.convBusyKey(ids[2])] = true
	s.chatBusy["unrelated-task"] = true
	s.chatMu.Unlock()
	check(map[int64]bool{ids[0]: false, ids[1]: true, ids[2]: true})
	s.chatMu.Lock()
	clear(s.chatBusy)
	s.chatMu.Unlock()
	check(map[int64]bool{ids[0]: false, ids[1]: false, ids[2]: false})
}

func TestConversationDeepLinkRetainsLatestOutcome(t *testing.T) {
	s, _ := newRetestServer(t)
	conversation, err := s.m.pg.CreateAutomationConversation("auto", "deep link", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.m.pg.DeleteConversation(conversation.ID)
	if _, err := s.m.pg.AppendConvActivity(conversation.ID, db.Activity{Kind: "result", IsError: true}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/conversations/%d", conversation.ID), nil)
	r.SetPathValue("id", fmt.Sprint(conversation.ID))
	w := httptest.NewRecorder()
	s.pgGetConversation(w, r)
	var item conversationListItem
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || item.ID != conversation.ID || item.Outcome != "failed" {
		t.Fatalf("deep link status=%d item=%+v", w.Code, item)
	}
}
