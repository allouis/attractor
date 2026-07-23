package acp

import (
	"fmt"
	"strings"
)

// parseCommandLine splits a shell-ish command string into leading
// NAME=value environment assignments, the program, and its arguments.
// Single and double quotes group words; no other shell features
// (globs, substitution, pipes) are supported — the command runs
// directly, not through a shell.
func parseCommandLine(raw string) (prog string, args, env []string, err error) {
	tokens, err := splitTokens(raw)
	if err != nil {
		return "", nil, nil, err
	}
	i := 0
	for ; i < len(tokens); i++ {
		if !isEnvAssignment(tokens[i]) {
			break
		}
		env = append(env, tokens[i])
	}
	if i == len(tokens) {
		return "", nil, nil, fmt.Errorf("acp: command %q has no program", raw)
	}
	return tokens[i], tokens[i+1:], env, nil
}

// isEnvAssignment reports whether tok looks like NAME=value with a
// plausible variable name before the first '='.
func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range tok[:eq] {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// splitTokens is a minimal quote-aware whitespace splitter.
func splitTokens(raw string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inTok := false
	quote := byte(0)
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			} else {
				cur.WriteByte(ch)
			}
		case ch == '\'' || ch == '"':
			quote = ch
			inTok = true
		case ch == ' ' || ch == '\t' || ch == '\n':
			if inTok {
				tokens = append(tokens, cur.String())
				cur.Reset()
				inTok = false
			}
		default:
			cur.WriteByte(ch)
			inTok = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("acp: unbalanced quote in command %q", raw)
	}
	if inTok {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
}
