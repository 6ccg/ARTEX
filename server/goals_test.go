package server

import (
	"context"
	"testing"
)

func TestCreateGoalsNilTask(t *testing.T) {
	if got := (&Server{Runtime: &Runtime{}}).createGoals(context.Background(), nil, nil); len(got) != 0 {
		t.Fatalf("nil task produced goals: %+v", got)
	}
}
