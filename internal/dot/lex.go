package dot

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokKind int

const (
	tkEOF tokKind = iota
	tkWord            // [A-Za-z_][A-Za-z0-9_]*  — single identifier segment
	tkBare            // identifier followed by '.' / ':' / '-' continuations
	tkNumber          // -?digits(.digits)?(durationSuffix)?
	tkString          // double-quoted with escapes
	tkArrow           // ->
	tkLBrace          // {
	tkRBrace          // }
	tkLBracket        // [
	tkRBracket        // ]
	tkComma           // ,
	tkSemi            // ;
	tkEquals          // =
)

// token carries lexical state for the parser. value holds the canonical
// payload: identifier text for tkWord/tkBare, decoded string for tkString,
// raw lexeme for tkNumber, etc.
type token struct {
	kind  tokKind
	value string
	line  int
	col   int
}

type lexer struct {
	src  string
	pos  int
	line int
	col  int
}

func newLexer(src string) *lexer {
	return &lexer{src: src, line: 1, col: 1}
}

func (l *lexer) peekRune() (rune, int) {
	if l.pos >= len(l.src) {
		return 0, 0
	}
	r, n := utf8.DecodeRuneInString(l.src[l.pos:])
	return r, n
}

func (l *lexer) advanceRune() rune {
	r, n := l.peekRune()
	l.pos += n
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

// skipWhitespaceAndComments advances past ASCII whitespace, `//` line
// comments, and `/* */` block comments. Block comments cannot nest, per
// the DOT grammar reference.
func (l *lexer) skipWhitespaceAndComments() error {
	for l.pos < len(l.src) {
		r, _ := l.peekRune()
		switch {
		case unicode.IsSpace(r):
			l.advanceRune()
		case r == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '/':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.advanceRune()
			}
		case r == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '*':
			startLine, startCol := l.line, l.col
			l.advanceRune()
			l.advanceRune()
			closed := false
			for l.pos+1 < len(l.src) {
				if l.src[l.pos] == '*' && l.src[l.pos+1] == '/' {
					l.advanceRune()
					l.advanceRune()
					closed = true
					break
				}
				l.advanceRune()
			}
			if !closed {
				return fmt.Errorf("line %d col %d: unterminated /* comment", startLine, startCol)
			}
		default:
			return nil
		}
	}
	return nil
}

// next emits the next token. EOF is signalled by tkEOF with empty value.
func (l *lexer) next() (token, error) {
	if err := l.skipWhitespaceAndComments(); err != nil {
		return token{}, err
	}
	if l.pos >= len(l.src) {
		return token{kind: tkEOF, line: l.line, col: l.col}, nil
	}
	startLine, startCol := l.line, l.col
	r, _ := l.peekRune()
	switch {
	case r == '{':
		l.advanceRune()
		return token{kind: tkLBrace, line: startLine, col: startCol}, nil
	case r == '}':
		l.advanceRune()
		return token{kind: tkRBrace, line: startLine, col: startCol}, nil
	case r == '[':
		l.advanceRune()
		return token{kind: tkLBracket, line: startLine, col: startCol}, nil
	case r == ']':
		l.advanceRune()
		return token{kind: tkRBracket, line: startLine, col: startCol}, nil
	case r == ',':
		l.advanceRune()
		return token{kind: tkComma, line: startLine, col: startCol}, nil
	case r == ';':
		l.advanceRune()
		return token{kind: tkSemi, line: startLine, col: startCol}, nil
	case r == '=':
		l.advanceRune()
		return token{kind: tkEquals, line: startLine, col: startCol}, nil
	case r == '"':
		return l.lexString(startLine, startCol)
	case r == '-':
		// either an arrow `->`, a negative number `-3`, or an illegal `--`.
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '>' {
			l.advanceRune()
			l.advanceRune()
			return token{kind: tkArrow, line: startLine, col: startCol}, nil
		}
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '-' {
			return token{}, fmt.Errorf("line %d col %d: undirected edges (`--`) are rejected", startLine, startCol)
		}
		if l.pos+1 < len(l.src) && (l.src[l.pos+1] >= '0' && l.src[l.pos+1] <= '9') {
			return l.lexNumber(startLine, startCol)
		}
		return token{}, fmt.Errorf("line %d col %d: unexpected `-`", startLine, startCol)
	case r >= '0' && r <= '9':
		return l.lexNumber(startLine, startCol)
	case unicode.IsLetter(r) || r == '_':
		return l.lexIdent(startLine, startCol)
	}
	return token{}, fmt.Errorf("line %d col %d: unexpected character %q", startLine, startCol, r)
}

func (l *lexer) lexString(line, col int) (token, error) {
	l.advanceRune() // consume opening "
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '"' {
			l.advanceRune()
			return token{kind: tkString, value: b.String(), line: line, col: col}, nil
		}
		if c == '\\' {
			l.advanceRune()
			if l.pos >= len(l.src) {
				return token{}, fmt.Errorf("line %d col %d: dangling escape", line, col)
			}
			esc := l.src[l.pos]
			l.advanceRune()
			switch esc {
			case '"':
				b.WriteByte('"')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '\\':
				b.WriteByte('\\')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte('\\')
				b.WriteByte(esc)
			}
			continue
		}
		l.advanceRune()
		b.WriteByte(c)
	}
	return token{}, fmt.Errorf("line %d col %d: unterminated string", line, col)
}

func (l *lexer) lexNumber(line, col int) (token, error) {
	start := l.pos
	if l.src[l.pos] == '-' {
		l.advanceRune()
	}
	for l.pos < len(l.src) && (l.src[l.pos] >= '0' && l.src[l.pos] <= '9') {
		l.advanceRune()
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' && l.pos+1 < len(l.src) &&
		l.src[l.pos+1] >= '0' && l.src[l.pos+1] <= '9' {
		l.advanceRune()
		for l.pos < len(l.src) && (l.src[l.pos] >= '0' && l.src[l.pos] <= '9') {
			l.advanceRune()
		}
	}
	// duration suffix
	for _, suf := range []string{"ms", "s", "m", "h", "d"} {
		if strings.HasPrefix(l.src[l.pos:], suf) {
			next := l.pos + len(suf)
			if next == len(l.src) || !isIdentContinuation(rune(l.src[next])) {
				for i := 0; i < len(suf); i++ {
					l.advanceRune()
				}
				break
			}
		}
	}
	return token{kind: tkNumber, value: l.src[start:l.pos], line: line, col: col}, nil
}

// lexIdent reads either a single identifier (`[A-Za-z_][A-Za-z0-9_]*`) or
// an extended bare word with `.` / `:` / `-` continuations (used for
// qualified attribute keys and bare values per spec §2.4). Distinguishing
// the two is deferred to the parser via the token kind.
func (l *lexer) lexIdent(line, col int) (token, error) {
	start := l.pos
	for l.pos < len(l.src) {
		r, n := l.peekRune()
		if unicode.IsLetter(r) || (r >= '0' && r <= '9') || r == '_' {
			l.pos += n
			l.col++
			continue
		}
		break
	}
	simple := l.src[start:l.pos]
	extended := simple
	hasContinuation := false
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '.' || c == ':' || c == '-' {
			// Look-ahead: continuation must be followed by identifier
			// character so we don't swallow stand-alone punctuation.
			if l.pos+1 >= len(l.src) {
				break
			}
			next := rune(l.src[l.pos+1])
			if !unicode.IsLetter(next) && !(next >= '0' && next <= '9') && next != '_' {
				break
			}
			hasContinuation = true
			l.advanceRune()
			for l.pos < len(l.src) {
				r, n := l.peekRune()
				if unicode.IsLetter(r) || (r >= '0' && r <= '9') || r == '_' {
					l.pos += n
					l.col++
					continue
				}
				break
			}
			extended = l.src[start:l.pos]
			continue
		}
		break
	}
	kind := tkWord
	if hasContinuation {
		kind = tkBare
	}
	return token{kind: kind, value: extended, line: line, col: col}, nil
}

func isIdentContinuation(r rune) bool {
	return unicode.IsLetter(r) || (r >= '0' && r <= '9') || r == '_'
}
