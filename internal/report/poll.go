package report

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/allouis/attractor/internal/interviewer"
)

// ControlAnswer is one human-gate answer the daemon relays to the polling
// child (mirrors the daemon's control answer shape).
type ControlAnswer struct {
	Key   string `json:"key,omitempty"`
	Label string `json:"label,omitempty"`
	Text  string `json:"text,omitempty"`
}

// Control is the daemon→child control state (GET /control): whether to
// abort, and answers to questions the child asked, keyed by question id.
type Control struct {
	Cancel  bool                     `json:"cancel"`
	Answers map[string]ControlAnswer `json:"answers"`
}

// Control fetches the run's current control state from the daemon.
func (c *Client) Control() (Control, error) {
	var ctrl Control
	req, err := http.NewRequest(http.MethodGet, c.url("/control"), nil)
	if err != nil {
		return ctrl, err
	}
	resp, err := c.do(req)
	if err != nil {
		return ctrl, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return ctrl, fmt.Errorf("control: %s", resp.Status)
	}
	err = json.NewDecoder(resp.Body).Decode(&ctrl)
	return ctrl, err
}

// PollInterviewer answers human gates in a phone-home run by polling the
// daemon. The wait.human handler already emits the InterviewStarted event
// (which the child forwards, so the daemon presents the question); this
// interviewer polls GET /control until a human answers via the daemon, then
// returns that answer to the headless engine (spec §6 — the frontend
// submits, the engine's interviewer fetches).
type PollInterviewer struct {
	client   *Client
	interval time.Duration
}

// NewPollInterviewer returns a PollInterviewer polling every interval
// (default 1s when non-positive).
func NewPollInterviewer(c *Client, interval time.Duration) *PollInterviewer {
	if interval <= 0 {
		interval = time.Second
	}
	return &PollInterviewer{client: c, interval: interval}
}

// Ask blocks until the daemon reports an answer for q, or a cancel. A
// cancel resolves as SKIPPED so the run unwinds (the launcher also kills
// the VM on cancel).
func (p *PollInterviewer) Ask(q interviewer.Question) (interviewer.Answer, error) {
	var deadline time.Time
	if q.Timeout > 0 {
		deadline = time.Now().Add(q.Timeout)
	}
	for {
		ctrl, err := p.client.Control()
		if err == nil {
			if a, ok := ctrl.Answers[q.ID]; ok {
				return controlToAnswer(a), nil
			}
			if ctrl.Cancel {
				return interviewer.Answer{Value: interviewer.AnswerSkipped}, nil
			}
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			// The handler resolves the timeout (spec §6.5 default_choice).
			return interviewer.Answer{Value: interviewer.AnswerTimeout}, nil
		}
		time.Sleep(p.interval)
	}
}

// controlToAnswer reconstructs an interviewer.Answer from a relayed answer,
// mirroring the daemon's SubmitAnswer mapping.
func controlToAnswer(a ControlAnswer) interviewer.Answer {
	if a.Key != "" || a.Label != "" {
		return interviewer.Answer{
			Value:          interviewer.AnswerChoice,
			SelectedOption: &interviewer.Option{Key: a.Key, Label: a.Label},
		}
	}
	return interviewer.Answer{Value: interviewer.AnswerText, Text: a.Text}
}
