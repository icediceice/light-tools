package bash

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type task struct {
	id        string
	status    string
	created   time.Time
	updated   time.Time
	expires   time.Time
	result    map[string]any
	err       string
	cancel    context.CancelFunc
}

type TaskManager struct {
	mu      sync.Mutex
	tasks   map[string]*task
	ttl     time.Duration
	maximum int
}

func NewTaskManager() *TaskManager {
	return &TaskManager{tasks: make(map[string]*task), ttl: time.Hour, maximum: 64}
}

func (m *TaskManager) Start(run func(context.Context) (map[string]any, error)) (string, error) {
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", err
	}
	id := hex.EncodeToString(idBytes)
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	entry := &task{id: id, status: "queued", created: now, updated: now, expires: now.Add(m.ttl), cancel: cancel}
	m.mu.Lock()
	m.reapLocked()
	if len(m.tasks) >= m.maximum {
		m.mu.Unlock()
		cancel()
		return "", fmt.Errorf("local async task limit reached")
	}
	m.tasks[id] = entry
	m.mu.Unlock()
	go func() {
		m.mu.Lock()
		entry.status, entry.updated = "running", time.Now()
		m.mu.Unlock()
		result, err := run(ctx)
		m.mu.Lock()
		defer m.mu.Unlock()
		if ctx.Err() == context.Canceled {
			entry.status = "cancelled"
		} else if err != nil {
			entry.status, entry.err = "failed", err.Error()
		} else {
			entry.status, entry.result = "done", result
		}
		entry.updated, entry.expires = time.Now(), time.Now().Add(m.ttl)
	}()
	return id, nil
}

func (m *TaskManager) Status(id string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reapLocked()
	entry, ok := m.tasks[id]
	if !ok {
		return nil, fmt.Errorf("unknown or expired task_id")
	}
	return taskMetadata(entry), nil
}

func (m *TaskManager) Collect(id string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reapLocked()
	entry, ok := m.tasks[id]
	if !ok {
		return nil, fmt.Errorf("unknown or expired task_id")
	}
	response := taskMetadata(entry)
	if entry.status == "done" {
		response["result"] = entry.result
		delete(m.tasks, id)
	} else if entry.status == "failed" || entry.status == "cancelled" {
		if entry.err != "" {
			response["error"] = entry.err
		}
		delete(m.tasks, id)
	}
	return response, nil
}

func (m *TaskManager) Cancel(id string) (map[string]any, error) {
	m.mu.Lock()
	entry, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("unknown or expired task_id")
	}
	if entry.status == "queued" || entry.status == "running" {
		entry.cancel()
		entry.status, entry.updated = "cancelling", time.Now()
	}
	response := taskMetadata(entry)
	m.mu.Unlock()
	return response, nil
}

func (m *TaskManager) reapLocked() {
	now := time.Now()
	for id, entry := range m.tasks {
		if now.After(entry.expires) && entry.status != "running" && entry.status != "queued" && entry.status != "cancelling" {
			delete(m.tasks, id)
		}
	}
}

func taskMetadata(entry *task) map[string]any {
	return map[string]any{
		"task_id": entry.id, "status": entry.status,
		"created": entry.created.UTC(), "updated": entry.updated.UTC(),
	}
}
