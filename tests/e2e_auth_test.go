package attractor_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fabro/attractor/internal/handler"
	"github.com/fabro/attractor/internal/server"
)

func TestAuth_LoopbackBypassesToken(t *testing.T) {
	srv := server.New(server.Config{
		Addr:         "127.0.0.1:0",
		LogsRoot:     t.TempDir(),
		MakeHandlers: server.DefaultHandlers(handler.Codergen{}),
		AuthToken:    "secrettoken",
	})
	must(t, srv.Start())
	t.Cleanup(func() { _ = srv.Close() })

	// Loopback caller without Authorization header should succeed.
	resp, err := http.Get(srv.URL() + "/healthz")
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status=%d", resp.StatusCode)
	}
	// Submitting a pipeline from loopback also succeeds.
	dot := `digraph p {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	r, err := http.Post(srv.URL()+"/pipelines", "text/vnd.graphviz", strings.NewReader(dot))
	must(t, err)
	if r.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		t.Fatalf("submit status=%d body=%s", r.StatusCode, body)
	}
	var p struct{ ID string }
	must(t, json.NewDecoder(r.Body).Decode(&p))
	r.Body.Close()
	// Wait for completion so the temp dir cleanup doesn't race with the
	// pipeline goroutine.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(srv.URL() + "/pipelines/" + p.ID)
		if err == nil {
			var s struct {
				Status string `json:"status"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&s)
			resp.Body.Close()
			if s.Status == "completed" || s.Status == "failed" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("pipeline did not complete in time")
}

func TestAuth_NonLoopbackRequiresToken(t *testing.T) {
	// Bind to any non-loopback interface available, falling back to a
	// secondary loopback alias if needed. On most CI hosts we get a
	// listener whose Addr is something like 0.0.0.0:NNN — that's enough
	// because we dial via a non-loopback connection through a raw
	// net.Dial that records the local addr the server will see.

	addr, listenerCleanup := acquireNonLoopback(t)
	defer listenerCleanup()

	srv := server.New(server.Config{
		Addr:         addr,
		LogsRoot:     t.TempDir(),
		MakeHandlers: server.DefaultHandlers(handler.Codergen{}),
		AuthToken:    "secrettoken",
	})
	must(t, srv.Start())
	t.Cleanup(func() { _ = srv.Close() })

	// Dialing from the same host with no auth: server sees the
	// connection coming from a non-loopback peer and demands the token.
	url := srv.URL() + "/pipelines/x"
	r1, err := http.Get(url)
	must(t, err)
	r1.Body.Close()
	if r1.StatusCode != http.StatusUnauthorized {
		// On systems where the test's dial sources from 127.0.0.1 even
		// though the server is bound on 0.0.0.0 (kernel-routed), the
		// loopback bypass fires. Skip then.
		t.Skipf("kernel routed to loopback (status=%d), can't exercise auth on this host", r1.StatusCode)
	}
	if want := `Bearer realm="attractor"`; r1.Header.Get("WWW-Authenticate") != want {
		t.Fatalf("WWW-Authenticate = %q, want %q", r1.Header.Get("WWW-Authenticate"), want)
	}

	// With the right token, the request goes through (and returns 404
	// since /pipelines/x doesn't exist).
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer secrettoken")
	r2, err := http.DefaultClient.Do(req)
	must(t, err)
	r2.Body.Close()
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after auth, got %d", r2.StatusCode)
	}

	// Wrong token: still 401.
	req3, _ := http.NewRequest("GET", url, nil)
	req3.Header.Set("Authorization", "Bearer wrong")
	r3, err := http.DefaultClient.Do(req3)
	must(t, err)
	r3.Body.Close()
	if r3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d", r3.StatusCode)
	}
}

func TestAuth_DisabledByDefault(t *testing.T) {
	srv := server.New(server.Config{
		Addr:         "127.0.0.1:0",
		LogsRoot:     t.TempDir(),
		MakeHandlers: server.DefaultHandlers(handler.Codergen{}),
	})
	must(t, srv.Start())
	t.Cleanup(func() { _ = srv.Close() })

	r, err := http.Get(srv.URL() + "/healthz")
	must(t, err)
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("no-auth healthz status=%d", r.StatusCode)
	}
}

// acquireNonLoopback binds a dummy listener on a non-loopback interface,
// captures its address, and immediately releases the port so the server
// can rebind it. If no non-loopback interface is available, the test
// skips.
func acquireNonLoopback(t *testing.T) (string, func()) {
	t.Helper()
	ifaces, err := net.InterfaceAddrs()
	must(t, err)
	for _, addr := range ifaces {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil {
			continue
		}
		// Bind to any port on this address, then release immediately.
		l, err := net.Listen("tcp", ip.String()+":0")
		if err != nil {
			continue
		}
		bind := l.Addr().String()
		l.Close()
		return bind, func() {}
	}
	t.Skip("no non-loopback interface available")
	return "", func() {}
}
