package cli

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/runserver"
	"github.com/allouis/attractor/internal/runview"
)

// View re-serves a finished (or live) run directory read-only over the
// same auto loopback + tailnet UI binding `run --ui` uses. It takes a
// run-dir path — resolving a run id to its dir is what `runs` is for.
func View(args []string) error {
	fs := flag.NewFlagSet("view", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	uiAddr := fs.String("ui-addr", "127.0.0.1:0", "listen address; overrides the default auto behaviour (ephemeral loopback port, plus the Tailscale tailnet IP when present). A public/LAN --ui-addr requires --ui-token")
	uiToken := fs.String("ui-token", "", "require `Authorization: Bearer <token>` on the loopback and any public/LAN bind (mandatory for a public/LAN --ui-addr); the tailnet bind stays token-free")
	noTailnet := fs.Bool("no-tailnet", false, "serve only on loopback; do not bind the Tailscale tailnet IP. The tailnet bind is token-free, so use this to keep a sensitive run local-only")
	positional, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if len(positional) < 1 {
		return fmt.Errorf("view: expected a run directory path")
	}
	if len(positional) > 1 {
		return fmt.Errorf("view: unexpected extra arguments: %v", positional[1:])
	}
	dir := positional[0]
	if err := checkRunDir(dir); err != nil {
		return err
	}
	lns, _, err := serveView(dir, *uiAddr, flagSet(fs, "ui-addr"), *uiToken, !*noTailnet, hostTailnetIPs(), os.Stderr)
	if err != nil {
		return err
	}
	defer closeListeners(lns)

	// Block until interrupted; the run dir stays served for as long as the
	// operator wants to browse it.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	return nil
}

// checkRunDir rejects a path that is not a run directory before binding a
// port — a directory with neither run.json nor events.jsonl (a repo, the
// runs root, a typo) would otherwise serve an empty /pipelines.
func checkRunDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("view: %s is not a directory", dir)
	}
	if !fileExists(filepath.Join(dir, "run.json")) && !fileExists(filepath.Join(dir, "events.jsonl")) {
		return fmt.Errorf("view: %s is not a run directory (no run.json or events.jsonl)", dir)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// serveView is the non-blocking core of View: it stands up a runserver
// over dir (no engine attached, so the gate /answer endpoint returns 409)
// and serves it over the exact same bind path run --ui uses — via the
// shared serveRunUI, so dual-bind, trust classification, and the
// public-bind-needs-token rule all carry over with zero duplication.
// addTailnet mirrors `run --ui`: on by default (a tailnet peer can browse
// the run), but the tailnet bind is served token-free, so --no-tailnet
// (addTailnet=false) keeps a sensitive run on loopback only. Returns the
// live listeners and the primary (announce) listener.
func serveView(dir, uiAddr string, explicitSet bool, token string, addTailnet bool, tailnet []net.IP, warn io.Writer) ([]net.Listener, net.Listener, error) {
	srv := runserver.New(dir)
	srv.Token = token
	srv.Meta = viewMeta(dir)
	return serveRunUI("view", srv, uiAddr, explicitSet, addTailnet, tailnet, warn)
}

// viewMeta rebuilds the run server's node metadata by parsing the run
// dir's persisted graph.dot. graph.dot is render.TopologyDOT output —
// shape + label plus the type / llm_model / thread_id attributes — so the
// reconstruction is lossless: nodeMeta yields the same NodeMeta a live
// `run --ui` builds from the in-memory graph. It stays best-effort: a
// missing or unparseable graph.dot yields nil and the UI degrades
// gracefully (no lane grouping), never failing the view.
func viewMeta(dir string) map[string]runview.NodeMeta {
	src, err := os.ReadFile(filepath.Join(dir, "graph.dot"))
	if err != nil {
		return nil
	}
	file, err := dot.Parse(string(src))
	if err != nil {
		return nil
	}
	g, err := graph.Build(file)
	if err != nil {
		return nil
	}
	return nodeMeta(g)
}
