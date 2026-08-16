package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type asyncTask struct {
	status string
	result any
	err    string
	cancel context.CancelFunc
}

type taskStore struct {
	mu    sync.Mutex
	tasks map[string]*asyncTask
}

func newTaskStore() *taskStore { return &taskStore{tasks: make(map[string]*asyncTask)} }

func (s *taskStore) start(run func(context.Context) (any, error)) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	id := hex.EncodeToString(value)
	ctx, cancel := context.WithCancel(context.Background())
	entry := &asyncTask{status: "queued", cancel: cancel}
	s.mu.Lock()
	if len(s.tasks) >= 32 {
		s.mu.Unlock()
		cancel()
		return "", fmt.Errorf("local ops async task limit reached")
	}
	s.tasks[id] = entry
	s.mu.Unlock()
	go func() {
		s.mu.Lock()
		entry.status = "running"
		s.mu.Unlock()
		result, err := run(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		if ctx.Err() == context.Canceled {
			entry.status = "cancelled"
		} else if err != nil {
			entry.status, entry.err = "failed", err.Error()
		} else {
			entry.status, entry.result = "done", result
		}
	}()
	return id, nil
}

func (s *taskStore) action(verb, id string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tasks[id]
	if !ok {
		return nil, fmt.Errorf("unknown task_id")
	}
	if verb == "cancel" && (entry.status == "queued" || entry.status == "running") {
		entry.cancel()
		entry.status = "cancelling"
	}
	response := map[string]any{"task_id": id, "status": entry.status}
	if verb == "collect" && (entry.status == "done" || entry.status == "failed" || entry.status == "cancelled") {
		response["result"], response["error"] = entry.result, entry.err
		delete(s.tasks, id)
	}
	return response, nil
}

var _ = time.Second
