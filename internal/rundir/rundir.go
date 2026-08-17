// Package rundir holds the pure on-disk readers for a run directory:
// run.json and events.jsonl. They depend only on engine types, so a
// caller that merely lists or inspects runs (e.g. `attractor runs`) need
// not drag in the HTTP run server. Unlike the run server's per-request
// use — which swallows read errors and self-heals on the next poll —
// these return the error so a caller can tell a readable run from a
// half-written or corrupt one.
package rundir

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/allouis/attractor/internal/engine"
)

// ReadManifest reads and parses run.json. A missing file or malformed
// JSON returns the zero Manifest and a non-nil error; callers that want
// the run server's tolerant behaviour discard the error.
func ReadManifest(dir string) (engine.Manifest, error) {
	var m engine.Manifest
	data, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return engine.Manifest{}, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return engine.Manifest{}, err
	}
	return m, nil
}

// ReadEvents streams events.jsonl, returning events with Seq > since. It
// returns the events read so far alongside any error so the run server
// can keep its self-healing partial-read behaviour (discard the error)
// while a stricter caller can treat a corrupt log as untrusted:
//   - a missing or unreadable file (e.g. permission denied) → error;
//   - a scanner failure (an over-long >16 MiB or torn line) → error;
//   - a non-empty log whose every line fails to parse → error (the file
//     is garbage, not a fresh run that has yet to write an event).
//
// A single malformed line among valid ones is skipped without error —
// the poll model self-heals once the writer finishes the line.
func ReadEvents(dir string, since int64) ([]engine.Event, error) {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []engine.Event
	scannedAny, parsedAny := false, false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		scannedAny = true
		var ev engine.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		parsedAny = true
		if ev.Seq > since {
			out = append(out, ev)
		}
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	if scannedAny && !parsedAny {
		return out, errors.New("events.jsonl: no parseable events")
	}
	return out, nil
}
