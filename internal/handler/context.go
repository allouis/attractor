package handler

import (
	"fmt"
	"strings"

	"github.com/allouis/attractor/internal/engine"
)

// expandContext replaces runtime context placeholders in s from the live
// context (spec §4.5). It recognises two forms:
//
//   - `$context.<dotted.key>` — looked up exactly in the flat context
//     store. The key run is `[A-Za-z0-9_.]+` with trailing dots stripped
//     and the boundary at the first non-key byte. An undefined key is an
//     error (naming the key) so a mangled command fails loudly.
//   - `$goal` — the one spec built-in, sugar for `$context.graph.goal`.
//
// `$$` yields a literal `$`. Every other byte passes through untouched,
// so shell syntax (`$HOME`, `$(…)`, a lone `$`) survives.
func expandContext(s string, ctx *engine.Context) (string, error) {
	const ctxPrefix = "$context."
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if strings.HasPrefix(s[i:], "$$") {
			b.WriteByte('$')
			i += 2
			continue
		}
		if strings.HasPrefix(s[i:], "$goal") {
			b.WriteString(ctx.Get("graph.goal"))
			i += len("$goal")
			continue
		}
		if strings.HasPrefix(s[i:], ctxPrefix) {
			j := i + len(ctxPrefix)
			for j < len(s) && isContextKeyByte(s[j]) {
				j++
			}
			key := strings.TrimRight(s[i+len(ctxPrefix):j], ".")
			// A trailing dot is not part of the key; restart scanning
			// after the key so the dot passes through as a literal.
			j = i + len(ctxPrefix) + len(key)
			val, ok := ctx.Lookup(key)
			if !ok {
				return "", fmt.Errorf("unresolved $context.%s", key)
			}
			b.WriteString(val)
			i = j
			continue
		}
		// Bare `$` — leave it and the following byte for the shell.
		b.WriteByte('$')
		i++
	}
	return b.String(), nil
}

func isContextKeyByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_', c == '.':
		return true
	}
	return false
}
