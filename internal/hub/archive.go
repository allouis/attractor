package hub

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/allouis/attractor/internal/engine"
)

// TarRunDir writes dir's tree as a tar.gz stream (relative paths).
// Symlinks are skipped — a run dir archive is regular files only.
func TarRunDir(dir string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel) + "/", Mode: 0o755, Typeflag: tar.TypeDir})
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644, Size: info.Size()}); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// ShipArchive tars logsRoot and POSTs it to the hub's archive endpoint,
// completing the run's permanent record (finish → tar → ship → ack).
func ShipArchive(hubURL, runID, logsRoot string) error {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(TarRunDir(logsRoot, pw))
	}()
	req, err := http.NewRequest("POST", hubURL+"/pipelines/"+runID+"/archive", pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("hub archive: status %d", resp.StatusCode)
	}
	return nil
}

// Announce registers a run's URL (and optional access token) with the
// hub — one shot, at start.
func Announce(hubURL, runID, runURL string, token string) error {
	data, _ := json.Marshal(announceBody{RunID: runID, URL: runURL, Token: token})
	resp, err := http.Post(hubURL+"/announce", "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("hub announce: status %d", resp.StatusCode)
	}
	return nil
}

// readEventsFile parses an events.jsonl file.
func readEventsFile(path string) ([]engine.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []engine.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ev engine.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

// serveEventsSince streams events with Seq > since from a jsonl file.
func serveEventsSince(w io.Writer, path, sinceRaw string) {
	since, _ := strconv.ParseInt(sinceRaw, 10, 64)
	events, err := readEventsFile(path)
	if err != nil {
		return
	}
	enc := json.NewEncoder(w)
	for _, ev := range events {
		if ev.Seq > since {
			_ = enc.Encode(ev)
		}
	}
}
