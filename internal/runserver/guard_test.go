package runserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// reqStatus drives one request (with optional Host/Origin) through h.
func reqStatus(h http.Handler, method, target, host, origin, auth string) int {
	req := httptest.NewRequest(method, target, nil)
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code
}

func TestTokenlessSurfaceGuardsBrowser(t *testing.T) {
	s := New(t.TempDir())
	h := s.Handler(false) // tokenless: loopback/tailnet bind

	tests := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{"ip host, no origin", "100.64.0.9:8080", "", 200},
		{"localhost host", "localhost:9000", "", 200},
		{"hostname host rejected (DNS rebinding)", "evil.example.com:8080", "", 403},
		{"cross-origin rejected (CSRF)", "100.64.0.9:8080", "http://evil.example.com", 403},
		{"same-origin allowed", "100.64.0.9:8080", "http://100.64.0.9:8080", 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reqStatus(h, "GET", "http://x/healthz", tt.host, tt.origin, ""); got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

// The token-authenticated surface relies on the bearer token, not the
// browser guard, so a valid token passes regardless of Host/Origin.
func TestTokenSurfaceSkipsBrowserGuard(t *testing.T) {
	s := New(t.TempDir())
	s.Token = "secret"
	h := s.Handler(true)
	if got := reqStatus(h, "GET", "http://x/healthz", "evil.example.com", "http://evil.example.com", "Bearer secret"); got != 200 {
		t.Fatalf("token request through guard: %d, want 200", got)
	}
	if got := reqStatus(h, "GET", "http://x/healthz", "evil.example.com", "", ""); got != 401 {
		t.Fatalf("no token: %d, want 401", got)
	}
}
