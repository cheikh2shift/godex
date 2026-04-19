package scheduler

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduler_OnTaskFinishedCalled(t *testing.T) {
	dbPath := t.TempDir() + "/scheduler.db"
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	var called int32
	s.SetOnTaskFinished(func(*ScheduledTask) {
		atomic.AddInt32(&called, 1)
	})

	task := &ScheduledTask{
		ID:            "TEST",
		Prompt:        "noop",
		IntervalSec:   0,
		RunAt:         "",
		IsRepeating:   false,
		WorkingDir:    s.wd,
		CreatedAt:     time.Now(),
		ProviderType:  "current",
		ProviderName:  "current",
		ProviderModel: "current",
	}

	s.executeTask(task)

	if atomic.LoadInt32(&called) != 1 {
		t.Fatalf("expected callback called once, got %d", atomic.LoadInt32(&called))
	}
}
