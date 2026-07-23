// Package artifact implements Attractor's artifact store (spec §5.5):
// named typed storage for large stage outputs that don't belong in the
// run context. Small payloads (<100KB by default) live in memory; larger
// ones spill to `{base_dir}/<id>.json`.
package artifact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DefaultFileBackingThreshold is the spec-recommended cutover at which
// the store writes payloads to disk instead of holding them in memory.
const DefaultFileBackingThreshold = 100 * 1024

// Info describes a stored artefact (spec §5.5).
type Info struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	SizeBytes  int       `json:"size_bytes"`
	StoredAt   time.Time `json:"stored_at"`
	FileBacked bool      `json:"is_file_backed"`
}

// Store is the live artifact store for a run.
type Store struct {
	mu        sync.RWMutex
	baseDir   string
	threshold int
	mem       map[string][]byte
	info      map[string]Info
}

// New returns a Store rooted at baseDir. If baseDir is empty, file
// backing is disabled (all artefacts kept in memory regardless of
// size).
func New(baseDir string) *Store {
	return &Store{
		baseDir:   baseDir,
		threshold: DefaultFileBackingThreshold,
		mem:       map[string][]byte{},
		info:      map[string]Info{},
	}
}

// SetThreshold overrides the file-backing threshold. Mostly for tests.
func (s *Store) SetThreshold(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threshold = n
}

// Put stores raw bytes under the given ID + display name.
func (s *Store) Put(id, name string, data []byte) (Info, error) {
	if id == "" {
		return Info{}, fmt.Errorf("artifact: empty id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	info := Info{
		ID:        id,
		Name:      name,
		SizeBytes: len(data),
		StoredAt:  time.Now(),
	}
	if s.baseDir != "" && len(data) > s.threshold {
		if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
			return Info{}, err
		}
		path := filepath.Join(s.baseDir, id+".json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return Info{}, err
		}
		info.FileBacked = true
		delete(s.mem, id) // drop any memory copy
	} else {
		s.mem[id] = append([]byte{}, data...)
	}
	s.info[id] = info
	return info, nil
}

// PutJSON marshals v as JSON and stores it under id.
func (s *Store) PutJSON(id, name string, v any) (Info, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return Info{}, err
	}
	return s.Put(id, name, data)
}

// Get retrieves the raw bytes for id. For file-backed entries, the
// file is read on each call (callers cache if they need to).
func (s *Store) Get(id string) ([]byte, error) {
	s.mu.RLock()
	info, ok := s.info[id]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("artifact %q not found", id)
	}
	if info.FileBacked {
		return os.ReadFile(filepath.Join(s.baseDir, id+".json"))
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.mem[id]
	if !ok {
		return nil, fmt.Errorf("artifact %q missing in memory store", id)
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

// Has reports whether an artefact exists.
func (s *Store) Has(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.info[id]
	return ok
}

// List returns infos in lexicographic order by ID. Snapshot semantics.
func (s *Store) List() []Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.info))
	for k := range s.info {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Info, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.info[k])
	}
	return out
}

// Remove drops an artefact, deleting the backing file if applicable.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.info[id]
	if !ok {
		return fmt.Errorf("artifact %q not found", id)
	}
	delete(s.info, id)
	delete(s.mem, id)
	if info.FileBacked && s.baseDir != "" {
		_ = os.Remove(filepath.Join(s.baseDir, id+".json"))
	}
	return nil
}

// Clear removes every artefact and any backing files.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, info := range s.info {
		if info.FileBacked && s.baseDir != "" {
			_ = os.Remove(filepath.Join(s.baseDir, id+".json"))
		}
		delete(s.info, id)
	}
	s.mem = map[string][]byte{}
}
