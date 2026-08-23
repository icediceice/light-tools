// Package telemetry records local-only aggregates measuring how much
// conversation light-tools' bounded representations saved. It stores per-tool
// call counts, terse token savings, read-dedup byte savings, and write-payload
// byte savings. It never records a path, argument, command, hostname, or
// username, and it has no network component of any kind. Set DO_NOT_TRACK=1 or
// a non-empty LIGHT_NO_TELEMETRY to opt out entirely.
package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	// filePrefix and the grammar below define the ONLY filenames Load reads.
	// A store root is never empty (SCHEMA and .lock live in every root), so a
	// strict grammar is what makes a fresh read a defined zero.
	filePrefix = "session-v1-"
	fileSuffix = ".json"

	defaultFlushInterval = 30 * time.Second
	// retention is how long a retired session's final snapshot survives.
	retention = 30 * 24 * time.Hour
	// sessionCap is the hard maximum number of sessions kept on disk.
	sessionCap = 50
)

var filenamePattern = regexp.MustCompile(`^session-v1-([0-9a-f]{32})-([1-9][0-9]*)\.json$`)

// Recorder is the metrics sink used on tool hot paths. Implementations record
// into memory only: a call must never touch the filesystem, so a stalled disk
// cannot delay a tool result.
type Recorder interface {
	RecordCall(tool string)
	RecordTerseTokens(saved int)
	RecordDedupBytes(saved int)
	RecordWriteBytes(saved int)
}

// snapshot is the persisted, cumulative state of one session. Every generation
// carries the FULL session totals, so the highest generation per session is the
// single authoritative value and interrupted flushes never double-count.
type snapshot struct {
	Session          string           `json:"session"`
	Generation       int64            `json:"generation"`
	Updated          time.Time        `json:"updated"`
	Calls            map[string]int64 `json:"calls,omitempty"`
	TerseTokensSaved int64            `json:"terse_tokens_saved,omitempty"`
	DedupBytesSaved  int64            `json:"dedup_bytes_saved,omitempty"`
	WriteBytesSaved  int64            `json:"write_bytes_saved,omitempty"`
}

// Store is the in-memory recorder for one process plus the writer goroutine
// that owns every byte of telemetry disk I/O. A nil *Store is a valid no-op
// Recorder: New returns nil when telemetry is disabled.
type Store struct {
	dir     string
	session string

	mu             sync.Mutex
	current        snapshot
	version        int64 // bumped by every Record
	flushedVersion int64
	generation     int64

	flushInterval time.Duration
	now           func() time.Time

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// Enabled reports whether telemetry is on. It is consulted exactly once, at
// Store construction; the Record path never reads the environment.
func Enabled() bool {
	return os.Getenv("DO_NOT_TRACK") != "1" && os.Getenv("LIGHT_NO_TELEMETRY") == ""
}

// New returns a Store persisting snapshots into dir, or nil when telemetry is
// disabled by environment. The returned store starts one writer goroutine;
// Close stops it and flushes any pending counters.
func New(dir string) *Store {
	if !Enabled() {
		return nil
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil // telemetry must never take the server down with it
	}
	store := &Store{
		dir:           dir,
		session:       hex.EncodeToString(id),
		current:       snapshot{Session: hex.EncodeToString(id), Calls: make(map[string]int64)},
		flushInterval: defaultFlushInterval,
		now:           time.Now,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
	go store.writerLoop()
	return store
}

// Close stops the writer goroutine and flushes pending counters. It is safe to
// call more than once and on a nil store.
func (s *Store) Close() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
}

func (s *Store) RecordCall(tool string) {
	if s == nil || tool == "" {
		return
	}
	s.mu.Lock()
	s.current.Calls[tool]++
	s.version++
	s.mu.Unlock()
}

// Each Record method guards the nil receiver itself: the address-of argument
// below is evaluated at the call site, so recordTotal's own nil check cannot
// save a nil caller from dereferencing s.current.
func (s *Store) RecordTerseTokens(saved int) {
	if s == nil {
		return
	}
	s.recordTotal(&s.current.TerseTokensSaved, saved)
}

func (s *Store) RecordDedupBytes(saved int) {
	if s == nil {
		return
	}
	s.recordTotal(&s.current.DedupBytesSaved, saved)
}

func (s *Store) RecordWriteBytes(saved int) {
	if s == nil {
		return
	}
	s.recordTotal(&s.current.WriteBytesSaved, saved)
}

func (s *Store) recordTotal(field *int64, saved int) {
	if saved <= 0 {
		return
	}
	s.mu.Lock()
	*field += int64(saved)
	s.version++
	s.mu.Unlock()
}

func (s *Store) writerLoop() {
	defer close(s.done)
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			s.flush()
			return
		case <-ticker.C:
			s.flush()
		}
	}
}

// flush persists the current counters as a NEW generation, removes the prior
// generation, then prunes. All filesystem work happens here, on the writer
// goroutine, never on a Record path.
func (s *Store) flush() {
	s.mu.Lock()
	if s.version == s.flushedVersion {
		s.mu.Unlock()
		return // nothing recorded since the last durable snapshot
	}
	next := s.generation + 1
	copied := s.current
	copied.Generation = next
	copied.Updated = s.now().UTC()
	copiedVersion := s.version
	s.mu.Unlock()

	data, err := json.Marshal(copied)
	if err != nil {
		return
	}
	if err := writeSnapshot(s.dir, s.session, next, data); err != nil {
		return // keep flushedVersion behind; the next tick retries
	}
	s.mu.Lock()
	if copiedVersion > s.flushedVersion {
		s.flushedVersion = copiedVersion
	}
	s.generation = next
	s.mu.Unlock()
	if previous := snapshotPath(s.dir, s.session, next-1); next > 1 {
		_ = os.Remove(previous) // immutable finals: the old generation is superseded
	}
	s.prune()
}

func snapshotPath(dir, session string, generation int64) string {
	return filepath.Join(dir, fmt.Sprintf("%s%s-%d%s", filePrefix, session, generation, fileSuffix))
}

// writeSnapshot writes data through a temp file (fsync) and renames it onto a
// final name that did not exist, so an interrupted flush leaves either the old
// generation or the new one, never a half-written current file.
func writeSnapshot(dir, session string, generation int64, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	final := snapshotPath(dir, session, generation)
	if _, err := os.Lstat(final); err == nil {
		return fmt.Errorf("telemetry snapshot %s already exists", final)
	} else if !os.IsNotExist(err) {
		return err
	}
	temp, err := os.CreateTemp(dir, ".snapshot-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, final)
}

// prune enforces retention and the hard session cap. It belongs to the writer,
// never to Load: a UI read must not mutate the store or race live writers.
func (s *Store) prune() {
	now := s.now()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	newest := make(map[string]int64) // session id -> newest generation on disk
	modTimes := make(map[string]time.Time)
	for _, entry := range entries {
		match := filenamePattern.FindStringSubmatch(entry.Name())
		if match == nil || !entry.Type().IsRegular() {
			continue
		}
		session := match[1]
		generation, err := strconv.ParseInt(match[2], 10, 64)
		if err != nil {
			continue
		}
		if generation > newest[session] {
			newest[session] = generation
			if info, err := entry.Info(); err == nil {
				modTimes[session] = info.ModTime()
			}
		}
	}
	// Retention decides first, then the hard cap chooses among what retention
	// kept, so a session expired by retention is never counted (and evicted)
	// twice by the cap branch.
	victims := make(map[string]bool)
	for session := range newest {
		if session != s.session && now.Sub(modTimes[session]) > retention {
			victims[session] = true
		}
	}
	if kept := len(newest) - len(victims); kept > sessionCap {
		var survivors []string
		for session := range newest {
			if session != s.session && !victims[session] {
				survivors = append(survivors, session)
			}
		}
		sort.Slice(survivors, func(i, j int) bool { return modTimes[survivors[i]].Before(modTimes[survivors[j]]) })
		for _, session := range survivors[:kept-sessionCap] {
			victims[session] = true
		}
	}
	for session := range victims {
		for generation := int64(1); generation <= newest[session]; generation++ {
			_ = os.Remove(snapshotPath(s.dir, session, generation))
		}
	}
}

// Totals is the aggregate the vault UI renders: the sum of every retained
// session's newest snapshot. Because snapshots persist periodically, the totals
// are a persisted lower bound on the true session activity.
type Totals struct {
	Sessions         int              `json:"sessions"`
	Calls            map[string]int64 `json:"calls,omitempty"`
	TerseTokensSaved int64            `json:"terse_tokens_saved"`
	DedupBytesSaved  int64            `json:"dedup_bytes_saved"`
	WriteBytesSaved  int64            `json:"write_bytes_saved"`
	Warnings         []string         `json:"warnings,omitempty"`
}

// Load aggregates a telemetry directory. Only the strict snapshot grammar is
// considered; every other filename (SCHEMA, .lock, temp files) is ignored, so a
// fresh store reads as a clean zero. Per session only the highest generation
// counts, so an interrupted flush never double-counts. Files that match the
// grammar but fail to decode are skipped and reported as health warnings.
func Load(dir string) (Totals, error) {
	totals := Totals{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return totals, nil
	}
	if err != nil {
		return totals, err
	}
	type retained struct {
		data       snapshot
		generation int64
	}
	best := make(map[string]retained)
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		match := filenamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		session, generation := match[1], match[2]
		parsed, err := strconv.ParseInt(generation, 10, 64)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			totals.Warnings = append(totals.Warnings, fmt.Sprintf("%s: unreadable (%v)", entry.Name(), err))
			continue
		}
		var snap snapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			totals.Warnings = append(totals.Warnings, fmt.Sprintf("%s: malformed snapshot (%v)", entry.Name(), err))
			continue
		}
		if snap.Session != session || snap.Generation != parsed {
			totals.Warnings = append(totals.Warnings, fmt.Sprintf("%s: session/generation does not match its filename", entry.Name()))
			continue
		}
		if current, ok := best[session]; !ok || parsed > current.generation {
			best[session] = retained{data: snap, generation: parsed}
		}
	}
	totals.Sessions = len(best)
	for _, entry := range best {
		for tool, count := range entry.data.Calls {
			if totals.Calls == nil {
				totals.Calls = make(map[string]int64)
			}
			totals.Calls[tool] += count
		}
		totals.TerseTokensSaved += entry.data.TerseTokensSaved
		totals.DedupBytesSaved += entry.data.DedupBytesSaved
		totals.WriteBytesSaved += entry.data.WriteBytesSaved
	}
	return totals, nil
}
