package engine

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestEmitConcurrentWritesAreLineAtomic verifies that concurrent emit calls
// never interleave a payload and its trailing newline, so every line of
// events.jsonl remains a parseable JSON object. Run under -race.
func TestEmitConcurrentWritesAreLineAtomic(t *testing.T) {
	dir := t.TempDir()
	e := New(Config{LogsRoot: dir, RunID: "run-concurrent"})
	e.openEventsFile()
	defer e.closeEventsFile()

	const goroutines = 16
	const perGoroutine = 200
	// A long message widens the window for an interleaved write to corrupt a line.
	msg := strings.Repeat("x", 512)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				e.emit(Event{Kind: EventStageProgress, Message: msg})
			}
		}()
	}
	wg.Wait()
	e.closeEventsFile()

	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("open events.jsonl: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lines := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("line %d is not valid JSON: %v\nline: %q", lines+1, err, line)
		}
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if want := goroutines * perGoroutine; lines != want {
		t.Fatalf("got %d parseable lines, want %d", lines, want)
	}
}
