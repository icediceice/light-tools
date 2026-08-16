package bash

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaximumSpills = 64
	defaultMaximumBytes  = 64 * 1024 * 1024
)

type spillRecord struct {
	path    string
	created time.Time
	expires time.Time
}

type SpillStore struct {
	root     string
	ttl      time.Duration
	maximum  int
	maxBytes int
	mu       sync.Mutex
	records  map[string]spillRecord
}

func NewSpillStore(root string, ttl time.Duration) (*SpillStore, error) {
	if ttl <= 0 {
		ttl = time.Hour
	}
	processRoot := filepath.Join(root, fmt.Sprintf("process-%d", os.Getpid()))
	if err := os.MkdirAll(processRoot, 0o700); err != nil {
		return nil, err
	}
	return &SpillStore{
		root: processRoot, ttl: ttl, maximum: defaultMaximumSpills,
		maxBytes: defaultMaximumBytes, records: make(map[string]spillRecord),
	}, nil
}

func (s *SpillStore) Store(data []byte) (string, error) {
	if len(data) > s.maxBytes {
		return "", fmt.Errorf("spill exceeds %d byte limit", s.maxBytes)
	}
	s.mu.Lock()
	s.reapLocked()
	if len(s.records) >= s.maximum {
		s.mu.Unlock()
		return "", fmt.Errorf("spill limit reached")
	}
	s.mu.Unlock()

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", err
	}
	id := hex.EncodeToString(idBytes)
	path := filepath.Join(s.root, id+".gz")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	writer := gzip.NewWriter(file)
	_, writeErr := writer.Write(data)
	closeErr := writer.Close()
	fileErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return "", writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	if fileErr != nil {
		_ = os.Remove(path)
		return "", fileErr
	}
	now := time.Now()
	s.mu.Lock()
	s.reapLocked()
	if len(s.records) >= s.maximum {
		s.mu.Unlock()
		_ = os.Remove(path)
		return "", fmt.Errorf("spill limit reached")
	}
	s.records[id] = spillRecord{path: path, created: now, expires: now.Add(s.ttl)}
	s.mu.Unlock()
	return id, nil
}

func (s *SpillStore) Read(id, ranges string) (string, error) {
	s.mu.Lock()
	record, ok := s.records[id]
	if !ok || time.Now().After(record.expires) {
		s.mu.Unlock()
		return "", fmt.Errorf("unknown or expired spill_id")
	}
	info, err := os.Lstat(record.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		s.mu.Unlock()
		return "", fmt.Errorf("spill identity check failed")
	}
	s.mu.Unlock()
	file, err := os.Open(record.path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	if ranges == "" {
		return string(data), nil
	}
	return selectRanges(string(data), ranges)
}

func (s *SpillStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = make(map[string]spillRecord)
	return os.RemoveAll(s.root)
}

func (s *SpillStore) reapLocked() {
	now := time.Now()
	for id, record := range s.records {
		if now.After(record.expires) {
			_ = os.Remove(record.path)
			delete(s.records, id)
		}
	}
}

func selectRanges(value, specification string) (string, error) {
	lines := strings.Split(value, "\n")
	var output []string
	for _, part := range strings.Split(specification, ",") {
		startText, endText, found := strings.Cut(strings.TrimSpace(part), "-")
		start, err := strconv.Atoi(startText)
		if err != nil || start < 1 {
			return "", fmt.Errorf("invalid line range %q", part)
		}
		end := start
		if found {
			end, err = strconv.Atoi(endText)
			if err != nil || end < start {
				return "", fmt.Errorf("invalid line range %q", part)
			}
		}
		if start > len(lines) {
			continue
		}
		if end > len(lines) {
			end = len(lines)
		}
		output = append(output, lines[start-1:end]...)
	}
	return strings.Join(output, "\n"), nil
}
