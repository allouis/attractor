package interviewer

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Console is the stdin/stdout Interviewer used by `attractor run
// --interactive`. It prints the question and its options, reads a line
// from stdin, and resolves the response to the matching option key.
// Accepts the accelerator key, the full label, or a numeric index.
type Console struct {
	In  io.Reader
	Out io.Writer
}

// Ask renders the question on Out and parks until a line arrives on In.
func (c Console) Ask(q Question) (Answer, error) {
	in := c.In
	if in == nil {
		in = os.Stdin
	}
	out := c.Out
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "[?]", q.Text)
	for i, opt := range q.Options {
		fmt.Fprintf(out, "  %d) [%s] %s\n", i+1, opt.Key, opt.Label)
	}
	fmt.Fprint(out, "> ")
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil {
		return Answer{Value: AnswerSkipped}, fmt.Errorf("console interviewer: %w", err)
	}
	choice := strings.TrimSpace(line)
	// After a valid choice, offer an optional free-text note (reviewer
	// feedback surfaced to the revisited node as $context.human.note). It is
	// optional: a blank line or EOF (piped/non-interactive) yields no note.
	note := func() string {
		fmt.Fprint(out, "note (optional, enter to skip)> ")
		l, err := reader.ReadString('\n')
		if err != nil && l == "" {
			return ""
		}
		return strings.TrimSpace(l)
	}
	if choice == "" && len(q.Options) > 0 {
		opt := q.Options[0]
		return Answer{Value: AnswerChoice, SelectedOption: &opt, Text: note()}, nil
	}
	for i, opt := range q.Options {
		if strings.EqualFold(choice, opt.Key) || strings.EqualFold(choice, opt.Label) {
			o := opt
			return Answer{Value: AnswerChoice, SelectedOption: &o, Text: note()}, nil
		}
		if choice == fmt.Sprintf("%d", i+1) {
			o := opt
			return Answer{Value: AnswerChoice, SelectedOption: &o, Text: note()}, nil
		}
	}
	// Unrecognised input falls through to the first option to keep the
	// pipeline moving; callers can press Ctrl-C if that's wrong.
	if len(q.Options) > 0 {
		opt := q.Options[0]
		return Answer{Value: AnswerChoice, SelectedOption: &opt, Text: choice}, nil
	}
	return Answer{Value: AnswerText, Text: choice}, nil
}
