// Package toml holds the line-level primitives for the minimal TOML
// subset Attractor's config files use: `#` line comments,
// `key = "value"` pairs, and `[a.b.c]` table headers. It is intentionally
// tiny — no arrays, integers, or multi-line values — and dependency-free,
// matching the deliberately small config surface (service-spec §1, §5).
// Callers (internal/config) layer their own table
// semantics on top of these shared helpers.
package toml

import (
	"fmt"
	"strings"
)

// StripComment removes a trailing `#` comment that sits outside a quoted
// string and trims surrounding whitespace.
func StripComment(line string) string {
	quote := byte(0)
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == '#':
			return strings.TrimSpace(line[:i])
		}
	}
	return strings.TrimSpace(line)
}

// ParseKeyValue splits `key = "value"`, unquoting the value.
func ParseKeyValue(line string) (key, val string, err error) {
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return "", "", fmt.Errorf("expected key = value, got %q", line)
	}
	key = strings.TrimSpace(line[:eq])
	val = strings.TrimSpace(line[eq+1:])
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
		val = val[1 : len(val)-1]
	}
	return key, val, nil
}

// TableHeader parses a `[a.b.c]` header into its dotted path segments.
// The caller decides which paths are meaningful.
func TableHeader(line string) ([]string, error) {
	if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		return nil, fmt.Errorf("malformed table header %q", line)
	}
	inner := strings.TrimSpace(line[1 : len(line)-1])
	if inner == "" {
		return nil, fmt.Errorf("empty table header %q", line)
	}
	parts := strings.Split(inner, ".")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
		if parts[i] == "" {
			return nil, fmt.Errorf("empty segment in table header %q", line)
		}
	}
	return parts, nil
}
