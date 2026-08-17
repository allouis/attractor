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
	positional, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if len(positional) < 1 {
		return fmt.Errorf("view: expected a run directory path")
	}
	dir := positional[0]
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("view: %s is not a directory", dir)
	}
	lns, _, err := serveView(dir, *uiAddr, flagSet(fs, "ui-addr"), *uiToken, hostTailnetIPs(), os.Stderr)
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

// serveView is the non-blocking core of View: it stands up a runserver
// over dir (no engine attached, so the gate /answer endpoint returns 409)
// and serves it over the exact same bind path run --ui uses. Meta is
// best-effort rebuilt from the run dir's graph.dot so the UI keeps its
// lane grouping; any parse failure just omits it. Returns the live
// listeners and the primary (announce) listener.
func serveView(dir, uiAddr string, explicitSet bool, token string, tailnet []net.IP, warn io.Writer) ([]net.Listener, net.Listener, error) {
	srv := runserver.New(dir)
	srv.Token = token
	srv.Meta = viewMeta(dir)
	return bindAndServe("view", srv, uiBinds(uiAddr, explicitSet, tailnet, true), tailnet, warn)
}

// viewMeta rebuilds the run server's node metadata by parsing the run
// dir's persisted graph.dot. It is deliberately best-effort: a missing or
// unparseable graph.dot yields nil, and the UI degrades gracefully
// (losing only lane grouping), never failing the view.
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
