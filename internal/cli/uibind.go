package cli

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/allouis/attractor/internal/runserver"
)

// tailnetCGNAT is the 100.64.0.0/10 CGNAT range (RFC 6598) Tailscale
// assigns tailnet addresses from. It is only a detection filter for the
// interface scan (tailnetIfaceIPs); trust for a bind comes from the
// address being one of the host's own detected tailnet interface IPs, not
// from range membership alone. CGNAT is not Tailscale-exclusive — carrier
// NAT, other overlay VPNs (ZeroTier/Nebula), and some CNIs use it too — so
// any 100.64/10 interface yields an open, tokenless tailnet bind.
var tailnetCGNAT = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// bindKind is the role of a planned listener; it determines whether the
// bind is mandatory (announce target, fatal on failure) and whether it is
// served token-free. Modelling the role as one enum avoids the illegal
// states a bag of independent bools would admit.
type bindKind int

const (
	bindLoopback bindKind = iota // ephemeral loopback (auto)
	bindTailnet                  // ephemeral Tailscale tailnet IP (auto)
	bindExplicit                 // the user's --ui-addr
)

// uiBind is one planned listener.
type uiBind struct {
	Addr string
	Kind bindKind
}

// primary reports whether the bind is mandatory: the announce target, and
// fatal if it fails to listen. Loopback and explicit binds are primary; a
// secondary tailnet bind is best-effort.
func (b uiBind) primary() bool { return b.Kind != bindTailnet }

// bare reports whether the bind is served without token auth regardless of
// --ui-token. Only the tailnet bind is bare: it is private but a remote
// browser over the tailnet cannot send a bearer header. Loopback and
// explicit binds honour a token when one is set.
func (b uiBind) bare() bool { return b.Kind == bindTailnet }

// uiBinds plans the listeners for --ui. An explicit --ui-addr forces a
// single bind to exactly that address (its trust decided later from the
// address net.Listen actually binds). Otherwise it plans an ephemeral
// loopback bind and, when addTailnet is set and a tailnet IP was detected,
// one ephemeral bind on the first. addTailnet is separate from the tailnet
// set so the set can be computed for trust classification (an explicit
// tailnet --ui-addr) while the automatic second bind is suppressed — e.g.
// an announce-only run, which adds no extra port.
func uiBinds(explicitAddr string, explicitSet bool, tailnet []net.IP, addTailnet bool) []uiBind {
	if explicitSet {
		return []uiBind{{Addr: explicitAddr, Kind: bindExplicit}}
	}
	binds := []uiBind{{Addr: "127.0.0.1:0", Kind: bindLoopback}}
	if addTailnet && len(tailnet) > 0 {
		binds = append(binds, uiBind{Addr: net.JoinHostPort(tailnet[0].String(), "0"), Kind: bindTailnet})
	}
	return binds
}

// tailnetIfaceIPs returns the host's interface addresses (as reported by
// net.InterfaceAddrs) that fall in the tailnet CGNAT range, in interface
// order. This scan of local interfaces IS the tailnet-detection method:
// range membership on a real host interface, per the brief.
func tailnetIfaceIPs(addrs []net.Addr) []net.IP {
	var out []net.IP
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if isTailnetIP(ip) {
			out = append(out, ip.To4())
		}
	}
	return out
}

// isTailnetIP reports whether ip is an IPv4 address in the tailnet CGNAT
// range.
func isTailnetIP(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && tailnetCGNAT.Contains(ip4)
}

// ipIsPublic reports whether a bound address is reachable beyond the
// private, authenticated boundaries that need no --ui-token: loopback, and
// the host's detected tailnet interface IPs (tailnet). A CGNAT address is
// private ONLY when it is in that detected set — proving interface origin,
// the same check the auto path uses — so an explicit --ui-addr in the
// range that is not a detected tailnet interface (or a nonlocal bind) is
// public and needs a token. Callers pass the address net.Listen actually
// bound (ln.Addr), so trust derives from the bound address, never a
// pre-bind lookup that could drift. A nil IP is treated as public: fail
// closed.
func ipIsPublic(ip net.IP, tailnet []net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() {
		return false
	}
	for _, t := range tailnet {
		if t.Equal(ip) {
			return false
		}
	}
	return true
}

// hostTailnetIPs returns the host's tailnet interface addresses, or nil if
// the interfaces cannot be read (best-effort; a host with no tailnet
// interface plans loopback only).
func hostTailnetIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	return tailnetIfaceIPs(addrs)
}

// boundIP returns the concrete IP a listener actually bound to.
func boundIP(ln net.Listener) net.IP {
	if a, ok := ln.Addr().(*net.TCPAddr); ok {
		return a.IP
	}
	return nil
}

// serveRunUI plans the --ui listeners and serves them. tailnet classifies
// trust for every bind; addTailnet (the --ui flag) gates only the
// automatic second bind, so an announce-only run classifies an explicit
// tailnet --ui-addr the same as a --ui run while opening no extra port.
func serveRunUI(srv *runserver.Server, uiAddr string, explicitSet, addTailnet bool, tailnet []net.IP, warn io.Writer) ([]net.Listener, net.Listener, error) {
	return bindAndServe(srv, uiBinds(uiAddr, explicitSet, tailnet, addTailnet), tailnet, warn)
}

// bindAndServe listens on every planned bind and serves srv on each. Trust
// is decided from the address net.Listen actually bound (classified
// against the detected tailnet set): a public bind (not loopback/tailnet)
// with no token is a fatal startup error. The token wraps a bind only when
// the bind is not bare (loopback / explicit) and srv.Token is set — the
// bare tailnet bind is never token-wrapped, since a remote browser cannot
// send a bearer header. srv.Token is the single token carrier. The primary
// bind (loopback, or the explicit --ui-addr) is mandatory: its failure is
// fatal; a secondary tailnet bind failure is warned and dropped, so the
// run keeps serving loopback exactly as with no tailnet present. Returns
// the live listeners and the primary listener (hub announce target).
func bindAndServe(srv *runserver.Server, binds []uiBind, tailnet []net.IP, warn io.Writer) ([]net.Listener, net.Listener, error) {
	var lns []net.Listener
	var primary net.Listener
	for _, b := range binds {
		ln, err := net.Listen("tcp", b.Addr)
		if err != nil {
			if b.primary() {
				closeListeners(lns)
				return nil, nil, fmt.Errorf("run: --ui listen: %w", err)
			}
			fmt.Fprintf(warn, "run: --ui tailnet bind %s failed (skipped): %v\n", b.Addr, err)
			continue
		}
		if ipIsPublic(boundIP(ln), tailnet) && srv.Token == "" {
			closeListeners(append(lns, ln))
			return nil, nil, fmt.Errorf("run: --ui-addr %s is publicly reachable; set --ui-token", ln.Addr())
		}
		go serveListener(ln, srv.Handler(!b.bare()), warn)
		fmt.Fprintf(warn, "run: serving UI at http://%s/ui (API: /pipelines)\n", ln.Addr())
		lns = append(lns, ln)
		if b.primary() {
			primary = ln
		}
	}
	return lns, primary, nil
}

// serveListener serves h on ln through an http.Server with header/read/
// idle timeouts, so a reachable (now possibly remote/tailnet) peer cannot
// hold connections or headers open to exhaust fds/goroutines. WriteTimeout
// is deliberately left unset: the run's event stream is long-lived. A
// benign close on shutdown is net.ErrClosed and stays quiet.
func serveListener(ln net.Listener, h http.Handler, warn io.Writer) {
	server := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := server.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(warn, "run: --ui serve on %s stopped: %v\n", ln.Addr(), err)
	}
}

func closeListeners(lns []net.Listener) {
	for _, ln := range lns {
		_ = ln.Close()
	}
}
