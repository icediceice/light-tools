package bash

import (
	"context"
	"testing"
	"time"
)

func TestTaskLifecycle(t *testing.T) {
	manager := NewTaskManager()
	id, err := manager.Start(func(context.Context) (map[string]any, error) {
		return map[string]any{"exit_code": 0}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		status, err := manager.Status(id)
		if err != nil {
			t.Fatal(err)
		}
		if status["status"] == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	collected, err := manager.Collect(id)
	if err != nil {
		t.Fatal(err)
	}
	if collected["status"] != "done" || collected["result"].(map[string]any)["exit_code"] != 0 {
		t.Fatalf("unexpected collected result: %#v", collected)
	}
	if _, err := manager.Status(id); err == nil {
		t.Fatal("collection should consume a completed task")
	}
}

func TestTaskCancel(t *testing.T) {
	manager := NewTaskManager()
	id, err := manager.Start(func(ctx context.Context) (map[string]any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Cancel(id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		status, err := manager.Status(id)
		if err != nil {
			t.Fatal(err)
		}
		if status["status"] == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not cancel: %#v", status)
		}
		time.Sleep(time.Millisecond)
	}
}
