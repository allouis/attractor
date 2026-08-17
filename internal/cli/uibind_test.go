package cli

import (
	"bytes"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/runserver"
)

var httpClient = &http.Client{Timeout: 5 * time.Second}

// getStatus issues GET url (optionally with a bearer token) and returns
// the status code. The request Host is the IP:port of url and no Origin
// is sent, so it passes the tokenless browser guard.
func getStatus(t *testing.T, url, token string) int {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// occupiedAddr holds an ephemeral loopback port for the test so a second
// bind to it fails deterministically (EADDRINUSE).
func occupiedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func closeAllLns(lns []net.Listener) {
	for _, ln := range lns {
		ln.Close()
	}
}

// ipNet builds a *net.IPNet the way net.InterfaceAddrs reports one, so
// tests can fabricate interface addresses without a real interface.
func ipNet(cidr string) *net.IPNet {
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func ips(addrs ...string) []net.IP {
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, net.ParseIP(a))
	}
	return out
}

func TestTailnetIfaceIPs(t *testing.T) {
	tests := []struct {
		name  string
		addrs []net.Addr
		want  []string
	}{
		{"none", []net.Addr{ipNet("192.168.1.5/24"), ipNet("127.0.0.1/8")}, nil},
		{"one CGNAT", []net.Addr{ipNet("192.168.1.5/24"), ipNet("100.101.102.103/10")}, []string{"100.101.102.103"}},
		{"public and LAN rejected", []net.Addr{ipNet("10.0.0.4/8"), ipNet("8.8.8.8/32"), ipNet("172.16.0.1/12")}, nil},
		{"multiple CGNAT in order", []net.Addr{ipNet("100.64.0.1/10"), ipNet("100.127.255.254/10")}, []string{"100.64.0.1", "100.127.255.254"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotS []string
			for _, ip := range tailnetIfaceIPs(tt.addrs) {
				gotS = append(gotS, ip.String())
			}
			if !reflect.DeepEqual(gotS, tt.want) {
				t.Fatalf("tailnetIfaceIPs = %v, want %v", gotS, tt.want)
			}
		})
	}
}

func TestUIBinds(t *testing.T) {
	loopback := uiBind{Addr: "127.0.0.1:0", Kind: bindLoopback}
	tests := []struct {
		name        string
		explicit    string
		explicitSet bool
		tailnet     []net.IP
		addTailnet  bool
		want        []uiBind
	}{
		{name: "no tailnet: loopback only", addTailnet: true, want: []uiBind{loopback}},
		{name: "tailnet present: dual bind", tailnet: ips("100.101.102.103"), addTailnet: true, want: []uiBind{loopback, {Addr: "100.101.102.103:0", Kind: bindTailnet}}},
		{name: "tailnet present but addTailnet off (announce-only): loopback only", tailnet: ips("100.101.102.103"), addTailnet: false, want: []uiBind{loopback}},
		{name: "multiple tailnet: only first bound", tailnet: ips("100.64.0.1", "100.127.255.254"), addTailnet: true, want: []uiBind{loopback, {Addr: "100.64.0.1:0", Kind: bindTailnet}}},
		{name: "explicit suppresses tailnet", explicit: "0.0.0.0:8080", explicitSet: true, tailnet: ips("100.101.102.103"), addTailnet: true, want: []uiBind{{Addr: "0.0.0.0:8080", Kind: bindExplicit}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := uiBinds(tt.explicit, tt.explicitSet, tt.tailnet, tt.addTailnet)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("uiBinds = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBindKindRoles(t *testing.T) {
	cases := []struct {
		kind    bindKind
		primary bool
		bare    bool
	}{
		{bindLoopback, true, false},
		{bindTailnet, false, true},
		{bindExplicit, true, false},
	}
	for _, c := range cases {
		b := uiBind{Kind: c.kind}
		if b.primary() != c.primary || b.bare() != c.bare {
			t.Errorf("kind %d: primary=%v bare=%v, want %v/%v", c.kind, b.primary(), b.bare(), c.primary, c.bare)
		}
	}
}

func TestIPIsPublic(t *testing.T) {
	tailnet := ips("100.64.0.9")
	tests := []struct {
		ip      string
		tailnet []net.IP
		want    bool
	}{
		{"127.0.0.1", nil, false},
		{"::1", nil, false},
		// A CGNAT address is public UNLESS it is one of the host's detected
		// tailnet interface IPs — range membership alone is not trust.
		{"100.64.0.9", nil, true},
		{"100.64.0.9", tailnet, false},
		{"100.64.0.1", tailnet, true},
		{"0.0.0.0", nil, true},
		{"::", nil, true},
		{"192.168.1.5", nil, true},
		{"8.8.8.8", nil, true},
	}
	for _, tt := range tests {
		if got := ipIsPublic(net.ParseIP(tt.ip), tt.tailnet); got != tt.want {
			t.Errorf("ipIsPublic(%s, %v) = %v, want %v", tt.ip, tt.tailnet, got, tt.want)
		}
	}
	if !ipIsPublic(nil, nil) {
		t.Errorf("ipIsPublic(nil) = false, want true (fail closed)")
	}
}

func TestBindAndServeDualBindAndPrimary(t *testing.T) {
	srv := runserver.New(t.TempDir())
	var buf bytes.Buffer
	binds := []uiBind{{Addr: "127.0.0.1:0", Kind: bindLoopback}, {Addr: "127.0.0.1:0", Kind: bindTailnet}}
	lns, primary, err := bindAndServe(srv, binds, nil, &buf)
	if err != nil {
		t.Fatalf("bindAndServe: %v", err)
	}
	defer closeAllLns(lns)
	if len(lns) != 2 || primary != lns[0] {
		t.Fatalf("got %d listeners, primary=%v; want 2 with primary=lns[0]", len(lns), primary)
	}
	if n := strings.Count(buf.String(), "serving UI at http://"); n != 2 {
		t.Fatalf("printed %d URLs, want 2:\n%s", n, buf.String())
	}
	for _, ln := range lns {
		if got := getStatus(t, "http://"+ln.Addr().String()+"/healthz", ""); got != 200 {
			t.Fatalf("healthz on %s: %d, want 200", ln.Addr(), got)
		}
	}
}

// BLOCK 1: an explicit --ui-token is enforced on the loopback bind.
func TestBindAndServeLoopbackEnforcesToken(t *testing.T) {
	srv := runserver.New(t.TempDir())
	srv.Token = "secret"
	var buf bytes.Buffer
	lns, _, err := bindAndServe(srv, []uiBind{{Addr: "127.0.0.1:0", Kind: bindLoopback}}, nil, &buf)
	if err != nil {
		t.Fatalf("bindAndServe: %v", err)
	}
	defer closeAllLns(lns)
	url := "http://" + lns[0].Addr().String() + "/healthz"
	if got := getStatus(t, url, ""); got != 401 {
		t.Fatalf("loopback with token, no header: %d, want 401", got)
	}
	if got := getStatus(t, url, "secret"); got != 200 {
		t.Fatalf("loopback with token: %d, want 200", got)
	}
}

// The tailnet bind stays token-free even when a token is set (a remote
// browser cannot send a bearer header).
func TestBindAndServeTailnetBindStaysBare(t *testing.T) {
	srv := runserver.New(t.TempDir())
	srv.Token = "secret"
	var buf bytes.Buffer
	lns, _, err := bindAndServe(srv, []uiBind{{Addr: "127.0.0.1:0", Kind: bindTailnet}}, nil, &buf)
	if err != nil {
		t.Fatalf("bindAndServe: %v", err)
	}
	defer closeAllLns(lns)
	if got := getStatus(t, "http://"+lns[0].Addr().String()+"/healthz", ""); got != 200 {
		t.Fatalf("tailnet bind should be bare: %d, want 200", got)
	}
}

func TestBindAndServeZeroConfigLoopbackTokenless(t *testing.T) {
	srv := runserver.New(t.TempDir())
	var buf bytes.Buffer
	lns, _, err := bindAndServe(srv, []uiBind{{Addr: "127.0.0.1:0", Kind: bindLoopback}}, nil, &buf)
	if err != nil {
		t.Fatalf("bindAndServe: %v", err)
	}
	defer closeAllLns(lns)
	if got := getStatus(t, "http://"+lns[0].Addr().String()+"/healthz", ""); got != 200 {
		t.Fatalf("zero-config loopback: %d, want 200", got)
	}
}

func TestBindAndServePublicBindNeedsToken(t *testing.T) {
	srv := runserver.New(t.TempDir())
	var buf bytes.Buffer
	binds := []uiBind{{Addr: "0.0.0.0:0", Kind: bindExplicit}}
	lns, _, err := bindAndServe(srv, binds, nil, &buf)
	if err == nil {
		closeAllLns(lns)
		t.Fatalf("public bind without token: no error; want one")
	}
	if !strings.Contains(err.Error(), "--ui-token") {
		t.Fatalf("error should mention --ui-token: %v", err)
	}
}

func TestBindAndServePublicBindWithToken(t *testing.T) {
	srv := runserver.New(t.TempDir())
	srv.Token = "secret"
	var buf bytes.Buffer
	lns, _, err := bindAndServe(srv, []uiBind{{Addr: "0.0.0.0:0", Kind: bindExplicit}}, nil, &buf)
	if err != nil {
		t.Fatalf("bindAndServe: %v", err)
	}
	defer closeAllLns(lns)
	_, port, _ := net.SplitHostPort(lns[0].Addr().String())
	url := "http://127.0.0.1:" + port + "/healthz"
	if got := getStatus(t, url, ""); got != 401 {
		t.Fatalf("public bind without token: %d, want 401", got)
	}
	if got := getStatus(t, url, "secret"); got != 200 {
		t.Fatalf("public bind with token: %d, want 200", got)
	}
}

func TestBindAndServeSecondaryBindFailureNonFatal(t *testing.T) {
	srv := runserver.New(t.TempDir())
	var buf bytes.Buffer
	busy := occupiedAddr(t)
	binds := []uiBind{{Addr: "127.0.0.1:0", Kind: bindLoopback}, {Addr: busy, Kind: bindTailnet}}
	lns, primary, err := bindAndServe(srv, binds, nil, &buf)
	if err != nil {
		t.Fatalf("bindAndServe returned error for a droppable bind: %v", err)
	}
	defer closeAllLns(lns)
	if len(lns) != 1 || primary != lns[0] {
		t.Fatalf("got %d listeners; loopback must survive a tailnet bind failure", len(lns))
	}
	if !strings.Contains(buf.String(), busy) {
		t.Fatalf("dropped bind not warned:\n%s", buf.String())
	}
}

func TestBindAndServePrimaryBindFailureFatal(t *testing.T) {
	srv := runserver.New(t.TempDir())
	var buf bytes.Buffer
	busy := occupiedAddr(t)
	lns, _, err := bindAndServe(srv, []uiBind{{Addr: busy, Kind: bindLoopback}}, nil, &buf)
	if err == nil {
		closeAllLns(lns)
		t.Fatalf("primary bind failure must be fatal")
	}
}

// TestServeRunUIWiring pins the Run-level glue: the tailnet set feeds both
// the plan and the trust classification, --ui gates the automatic second
// bind, and the loopback bind is the primary (announce) target. A loopback
// IP stands in for the tailnet address so the second bind is bindable in
// CI.
func TestServeRunUIWiring(t *testing.T) {
	fakeTailnet := ips("127.0.0.1")

	// --ui present: dual bind, primary is the loopback (announce target).
	t.Run("dual bind when tailnet present", func(t *testing.T) {
		srv := runserver.New(t.TempDir())
		var buf bytes.Buffer
		lns, primary, err := serveRunUI(srv, "127.0.0.1:0", false, true, fakeTailnet, &buf)
		if err != nil {
			t.Fatalf("serveRunUI: %v", err)
		}
		defer closeAllLns(lns)
		if len(lns) != 2 {
			t.Fatalf("got %d listeners, want 2 (loopback + tailnet)", len(lns))
		}
		if primary != lns[0] {
			t.Fatalf("announce target is not the loopback bind")
		}
	})

	// announce-only (addTailnet false): the automatic second bind is
	// suppressed even though a tailnet is present.
	t.Run("announce-only suppresses tailnet bind", func(t *testing.T) {
		srv := runserver.New(t.TempDir())
		var buf bytes.Buffer
		lns, primary, err := serveRunUI(srv, "127.0.0.1:0", false, false, fakeTailnet, &buf)
		if err != nil {
			t.Fatalf("serveRunUI: %v", err)
		}
		defer closeAllLns(lns)
		if len(lns) != 1 || primary != lns[0] {
			t.Fatalf("got %d listeners, want 1 (loopback only)", len(lns))
		}
	})
}
