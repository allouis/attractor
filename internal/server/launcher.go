package server

import (
	"fmt"
	"strings"
)

// Launcher provisions where a run executes and arranges for its events
// and artifacts to reach the daemon. Launch blocks for the run's duration
// so the dispatcher slot is held until the run finishes.
//
// The `direct` launcher runs the engine in the daemon process (today's
// path). `local` and `vm` launchers spawn `attractor run --report-to` in a
// subprocess / VM that phones home over HTTP; reportURL is the daemon base
// URL they report to (ignored by `direct`).
type Launcher interface {
	Launch(run *Run, reportURL string) error
}

// launcherFor returns the launcher for a run's placement: the named
// launcher when the run requested one that exists, else the default. This
// lets a single daemon run most pipelines one way while a submission can
// override per run (e.g. `{"runner":"vm"}`) for testing.
func (s *Server) launcherFor(placement string) Launcher {
	if placement != "" {
		if l, ok := s.launchers[placement]; ok {
			return l
		}
	}
	return s.launcher
}

// NewDirectLauncher returns the in-process launcher (registry Run.execute).
func NewDirectLauncher() Launcher { return directLauncher{} }

// directLauncher runs the engine in-process (registry Run.execute). It is
// the default so existing behavior and tests are unchanged (decision D5).
type directLauncher struct{}

func (directLauncher) Launch(run *Run, _ string) error {
	run.execute()
	return nil
}

// reportURL is the daemon base URL a phone-home child reports to, derived
// from the live listener address. Bare ":port" / "0.0.0.0" host resolves
// to loopback for a local child.
func (s *Server) reportURL() string {
	addr := s.addr
	host, port, found := strings.Cut(addr, ":")
	if !found {
		return "http://" + addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}
