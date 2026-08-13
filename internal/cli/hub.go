package cli

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/allouis/attractor/internal/hub"
)

// HubCmd runs the pull-based hub (local-first plan Phase 4): a
// directory + scraper + archive for self-contained runs. Runs started
// with `attractor run --announce <hub-url>` register here; the hub
// scrapes their own APIs and stores their completion archives as the
// permanent record. It never ingests a pushed event stream.
func HubCmd(args []string) error {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	bind := fs.String("bind", "127.0.0.1:7690", "TCP bind address")
	dir := fs.String("dir", "", "state root for run archives (default: $XDG_DATA_HOME/attractor/hub or ~/.attractor/hub)")
	interval := fs.Duration("scrape-interval", 2*time.Second, "how often live runs are scraped")
	launch := fs.Bool("launch", true, "enable POST /pipelines to spawn runs (attractor run --announce subprocesses)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root := *dir
	if root == "" {
		root = filepath.Join(defaultLogsRoot(), "..", "hub")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	h := hub.New(root)
	ln, err := net.Listen("tcp", *bind)
	if err != nil {
		return err
	}
	defer ln.Close()
	if *launch {
		h.Launcher = &hub.Launcher{
			HubURL:   "http://" + ln.Addr().String(),
			LogsBase: filepath.Join(root, "launched"),
		}
		if err := os.MkdirAll(h.Launcher.LogsBase, 0o755); err != nil {
			return err
		}
	}
	stop := make(chan struct{})
	defer close(stop)
	go h.Run(stop, *interval)
	fmt.Fprintf(os.Stderr, "hub: listening on http://%s (archives under %s)\n", ln.Addr(), root)
	return http.Serve(ln, h.Handler())
}
