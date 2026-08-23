package ops

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSourceQualifiedAmbiguity(t *testing.T) {
	registry := &Registry{
		services: []Service{
			{ID: "systemd:api", Source: "systemd", Name: "api"},
			{ID: "docker:api", Source: "docker", Name: "api"},
		},
		updated: time.Now(),
	}
	if _, err := registry.Resolve(context.Background(), "api"); err == nil || !strings.Contains(err.Error(), "systemd:api") || !strings.Contains(err.Error(), "docker:api") {
		t.Fatalf("expected candidate ids, got %v", err)
	}
	service, err := registry.Resolve(context.Background(), "docker:api")
	if err != nil || service.Source != "docker" {
		t.Fatalf("qualified id failed: %#v %v", service, err)
	}
}
