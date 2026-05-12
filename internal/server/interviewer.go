package server

import (
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
	q.ID = qid
	select {
	case a := <-ch:
		return a, nil
	case <-time.After(24 * time.Hour):
		return interviewer.Answer{Value: interviewer.AnswerTimeout}, nil
	}
}
