package payload

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	fileop "github.com/icediceice/light-tools/internal/file"
)

type Partial struct {
	Status   string `json:"status"`
	Stage    string `json:"stage"`
	GotLines int    `json:"got_lines"`
	Error    string `json:"error"`
}

type stageEntry struct {
	input   string
	lines   int
	expires time.Time
}

type Assembler struct {
	mu      sync.Mutex
	stages  map[string]stageEntry
	ttl     time.Duration
	maximum int
}

func NewAssembler() *Assembler {
	return &Assembler{stages: make(map[string]stageEntry), ttl: 15 * time.Minute, maximum: 32}
}

func (a *Assembler) Assemble(input string) ([]fileop.Mutation, *Partial, error) {
	stageID, fromLine, remainder, resuming, err := resumeHeaders(input)
	if err != nil {
		return nil, nil, err
	}
	combined := input
	if resuming {
		a.mu.Lock()
		a.reapLocked()
		entry, ok := a.stages[stageID]
		a.mu.Unlock()
		if !ok {
			return nil, nil, fmt.Errorf("unknown or expired payload stage %q", stageID)
		}
		if fromLine != entry.lines+1 {
			return nil, nil, fmt.Errorf("@from_line must be %d for stage %s", entry.lines+1, stageID)
		}
		combined = entry.input
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += remainder
	}
	mutations, parseErr := Parse(combined)
	if parseErr == nil {
		if resuming {
			a.mu.Lock()
			delete(a.stages, stageID)
			a.mu.Unlock()
		}
		return mutations, nil, nil
	}
	var partialError *PartialError
	if !errors.As(parseErr, &partialError) {
		return nil, nil, parseErr
	}
	lines := strings.Count(strings.ReplaceAll(combined, "\r\n", "\n"), "\n") + 1
	if !resuming {
		stageID, err = randomStageID()
		if err != nil {
			return nil, nil, err
		}
	}
	a.mu.Lock()
	a.reapLocked()
	if len(a.stages) >= a.maximum {
		a.evictOldestLocked()
	}
	a.stages[stageID] = stageEntry{input: combined, lines: lines, expires: time.Now().Add(a.ttl)}
	a.mu.Unlock()
	return nil, &Partial{Status: "partial", Stage: stageID, GotLines: lines, Error: partialError.Error()}, nil
}

func resumeHeaders(input string) (string, int, string, bool, error) {
	normalized := strings.ReplaceAll(input, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "@stage ") {
		return "", 0, input, false, nil
	}
	stage := strings.TrimSpace(strings.TrimPrefix(lines[0], "@stage "))
	if stage == "" || len(lines) < 2 || !strings.HasPrefix(lines[1], "@from_line ") {
		return "", 0, "", false, fmt.Errorf("stage resume requires @stage then @from_line")
	}
	from, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lines[1], "@from_line ")))
	if err != nil || from < 1 {
		return "", 0, "", false, fmt.Errorf("@from_line requires a positive integer")
	}
	return stage, from, strings.Join(lines[2:], "\n"), true, nil
}

func randomStageID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (a *Assembler) reapLocked() {
	now := time.Now()
	for id, entry := range a.stages {
		if now.After(entry.expires) {
			delete(a.stages, id)
		}
	}
}

func (a *Assembler) evictOldestLocked() {
	var oldestID string
	var oldest time.Time
	for id, entry := range a.stages {
		if oldestID == "" || entry.expires.Before(oldest) {
			oldestID, oldest = id, entry.expires
		}
	}
	delete(a.stages, oldestID)
}
