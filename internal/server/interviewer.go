package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fabro/attractor/internal/interviewer"
)

// remoteInterviewer fulfils an Interviewer.Ask by registering the
// question with the Run, then blocking until a POST /answer arrives.
type remoteInterviewer struct {
	run *Run
}

// Ask satisfies interviewer.Interviewer.
func (r *remoteInterviewer) Ask(q interviewer.Question) (interviewer.Answer, error) {
	qid, ch := r.run.registerQuestion(q, q.Stage)
	// Tag the question so frontends can map by qid.
	q.ID = qid
	select {
	case a := <-ch:
		return a, nil
	case <-time.After(24 * time.Hour):
		return interviewer.Answer{Value: interviewer.AnswerTimeout}, nil
	}
}

// jsonDecode is a tiny helper used by registry.go; defined here so it
// uses the encoding/json import already pulled in.
func jsonDecode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// contextWithTimeout is the small wrapper used by server.go for graceful
// shutdown.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
