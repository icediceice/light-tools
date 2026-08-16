package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

type Service struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Name     string `json:"name"`
	State    string `json:"state,omitempty"`
	Status   string `json:"status,omitempty"`
	OutLog   string `json:"out_log,omitempty"`
	ErrorLog string `json:"error_log,omitempty"`
}

type Registry struct {
	mu       sync.Mutex
	services []Service
	updated  time.Time
}

func (r *Registry) Discover(ctx context.Context, refresh bool) ([]Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !refresh && len(r.services) > 0 && time.Since(r.updated) < 30*time.Second {
		return append([]Service(nil), r.services...), nil
	}
	var services []Service
	services = append(services, discoverSystemd(ctx)...)
	services = append(services, discoverPM2(ctx)...)
	services = append(services, discoverDocker(ctx)...)
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })
	r.services, r.updated = services, time.Now()
	return append([]Service(nil), services...), nil
}

func (r *Registry) Resolve(ctx context.Context, name string) (Service, error) {
	services, err := r.Discover(ctx, false)
	if err != nil {
		return Service{}, err
	}
	for _, service := range services {
		if service.ID == name {
			return service, nil
		}
	}
	var matches []Service
	for _, service := range services {
		if service.Name == name {
			matches = append(matches, service)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return Service{}, fmt.Errorf("service %q not found", name)
	}
	ids := make([]string, len(matches))
	for index, service := range matches {
		ids[index] = service.ID
	}
	return Service{}, fmt.Errorf("ambiguous service %q; use one of: %s", name, strings.Join(ids, ", "))
}

func discoverSystemd(ctx context.Context) []Service {
	output, err := commandOutput(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain")
	if err != nil {
		return nil
	}
	var result []Service
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ".service")
		result = append(result, Service{ID: "systemd:" + name, Source: "systemd", Name: name, State: fields[2], Status: fields[3]})
	}
	return result
}

func discoverPM2(ctx context.Context) []Service {
	output, err := commandOutput(ctx, "pm2", "jlist")
	if err != nil {
		return nil
	}
	var rows []struct {
		Name string `json:"name"`
		PM2  struct {
			Status   string `json:"status"`
			OutLog   string `json:"pm_out_log_path"`
			ErrorLog string `json:"pm_err_log_path"`
		} `json:"pm2_env"`
	}
	if json.Unmarshal([]byte(output), &rows) != nil {
		return nil
	}
	result := make([]Service, 0, len(rows))
	for _, row := range rows {
		result = append(result, Service{ID: "pm2:" + row.Name, Source: "pm2", Name: row.Name, State: row.PM2.Status, OutLog: row.PM2.OutLog, ErrorLog: row.PM2.ErrorLog})
	}
	return result
}

func discoverDocker(ctx context.Context) []Service {
	output, err := commandOutput(ctx, "docker", "ps", "-a", "--format", "{{json .}}")
	if err != nil {
		return nil
	}
	var result []Service
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row struct {
			Names  string
			State  string
			Status string
		}
		if json.Unmarshal([]byte(line), &row) == nil && row.Names != "" {
			result = append(result, Service{ID: "docker:" + row.Names, Source: "docker", Name: row.Names, State: row.State, Status: row.Status})
		}
	}
	return result
}

func commandOutput(parent context.Context, executable string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, args...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		return "", err
	}
	return output.String(), nil
}
