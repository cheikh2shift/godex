package scheduler

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScheduler_ScopesTasksToWorkingDir(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "scheduler.db")

	wd1 := filepath.Join(tmp, "repo1")
	wd2 := filepath.Join(tmp, "repo2")

	// Create a repeating task scoped to wd1.
	s1, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s1.SetConfig(0, 0, wd1, "")
	if _, err := s1.AddTask("hello", 60, "", "current", "current", "current"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	s1.Close()

	// Re-open with the same wd; task should be visible and repeating.
	s2, err := New(dbPath)
	if err != nil {
		t.Fatalf("New (2): %v", err)
	}
	s2.SetConfig(0, 0, wd1, "")
	tasks := s2.ListTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task for wd1, got %d", len(tasks))
	}
	task, ok := tasks[0].(*ScheduledTask)
	if !ok {
		t.Fatalf("expected *ScheduledTask, got %T", tasks[0])
	}
	if task.WorkingDir != wd1 {
		t.Fatalf("expected WorkingDir=%q, got %q", wd1, task.WorkingDir)
	}
	if !task.IsRepeating {
		t.Fatalf("expected IsRepeating=true after reload")
	}
	s2.Close()

	// Re-open with a different wd; task should not be visible.
	s3, err := New(dbPath)
	if err != nil {
		t.Fatalf("New (3): %v", err)
	}
	s3.SetConfig(0, 0, wd2, "")
	if got := len(s3.ListTasks()); got != 0 {
		t.Fatalf("expected 0 tasks for wd2, got %d", got)
	}
	s3.Close()
}

func TestScheduler_DefaultWDUsesProcessWD(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "scheduler.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	pwd, _ := os.Getwd()
	if pwd != "" && s.wd != pwd {
		t.Fatalf("expected scheduler wd to default to process wd %q, got %q", pwd, s.wd)
	}
}
