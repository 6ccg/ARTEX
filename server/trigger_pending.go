package server

import (
	"log"

	"github.com/Autumn-27/artex/db"
)

// Persist each fire before it can advance a scheduler watermark.
func (s *Runtime) persistTrigger(item *triggeredRun) error {
	var id int64
	err := s.m.pg.QueryRow(`INSERT INTO pending_trigger_runs(agent_key,title,message,task_id,task_desc,task_goal,mergeable,conversation_id) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,0)) RETURNING id`, item.agentKey, item.title, item.message, item.taskID, item.taskDesc, item.taskGoal, item.mergeable, item.conversationID).Scan(&id)
	if err == nil {
		item.pendingIDs = []int64{id}
	}
	return err
}

// Restore also groups previously merged fires that share a conversation.
func (s *Runtime) restorePendingTriggers() {
	rows, err := s.m.pg.Query(`SELECT id,agent_key,title,message,task_id,task_desc,task_goal,mergeable,COALESCE(conversation_id,0),started FROM pending_trigger_runs ORDER BY id`)
	if err != nil {
		log.Printf("[trigger] load pending: %v", err)
		return
	}
	var items []triggeredRun
	groups := map[int64]int{}
	for rows.Next() {
		var id int64
		var item triggeredRun
		if err := rows.Scan(&id, &item.agentKey, &item.title, &item.message, &item.taskID, &item.taskDesc, &item.taskGoal, &item.mergeable, &item.conversationID, &item.started); err != nil {
			log.Printf("[trigger] load pending: %v", err)
			rows.Close()
			return
		}
		if item.started && item.conversationID != 0 {
			if i, ok := groups[item.conversationID]; ok {
				items[i].pendingIDs = append(items[i].pendingIDs, id)
				continue
			}
			groups[item.conversationID] = len(items)
		}
		item.pendingIDs = []int64{id}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		log.Printf("[trigger] load pending: %v", err)
		return
	}
	for _, item := range items {
		cfg := s.readTriggerBehavior(item.agentKey)
		s.queueMu.Lock()
		s.triggerCfg[item.agentKey] = cfg
		s.triggerQ[item.agentKey] = append(s.triggerQ[item.agentKey], item)
		s.queueMu.Unlock()
	}
	s.retryPendingTriggers()
}

func (s *Runtime) retryPendingTriggers() {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	for key := range s.triggerQ {
		s.pumpLocked(key)
	}
}

// Atomically create the initial turn and bind every member of a merge to it.
func (s *Runtime) bindPendingConversation(item *triggeredRun, msg string) (*db.Conversation, error) {
	if item.started {
		return s.m.pg.GetConversation(item.conversationID)
	}
	tx, err := s.m.pg.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id := item.conversationID
	if id == 0 {
		err = tx.QueryRow(`INSERT INTO conversations(agent_key,title,llm_profile_id) VALUES($1,$2,NULL) RETURNING id`, item.agentKey, firstLine(item.title, 60)).Scan(&id)
		if err != nil {
			return nil, err
		}
	}
	_, err = tx.Exec(`INSERT INTO conversation_activities(conversation_id,worker,kind,summary,detail) VALUES($1,$2,'user',$3,$4)`, id, item.agentKey, firstLine(msg, 200), msg)
	if err != nil {
		return nil, err
	}
	for _, pendingID := range item.pendingIDs {
		_, err = tx.Exec(`UPDATE pending_trigger_runs SET conversation_id=$2,started=true,title=$3,message=$4,task_desc='',task_goal='' WHERE id=$1`, pendingID, id, item.title, msg)
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	item.conversationID, item.started = id, true
	return s.m.pg.GetConversation(id)
}

func (s *Runtime) deletePendingTrigger(ids []int64) error {
	tx, err := s.m.pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM pending_trigger_runs WHERE id=$1`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
