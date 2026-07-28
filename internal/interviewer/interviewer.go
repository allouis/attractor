// Package interviewer defines the human-in-the-loop interface used by
// the wait.human handler (Attractor §6).
package interviewer

import (
	"errors"
	"sync"
)

// QuestionType enumerates the supported question presentations.
type QuestionType int

const (
	QuestionFreeform QuestionType = iota
	QuestionYesNo
	QuestionMultipleChoice
	QuestionConfirmation
)

// Option is a single multiple-choice option (spec §6.2).
type Option struct {
	Key   string
	Label string
}

// Question is presented to the human (spec §6.2).
type Question struct {
	ID       string
	Text     string
	Type     QuestionType
	Options  []Option
	Stage    string
	Metadata map[string]string
}

// AnswerValue is the enum used for non-text answers.
type AnswerValue int

const (
	AnswerYes AnswerValue = iota
	AnswerNo
	AnswerSkipped
	AnswerTimeout
	AnswerChoice
	AnswerText
)

// Answer is the response to a Question.
type Answer struct {
	Value          AnswerValue
	SelectedOption *Option
	Text           string
}

// Interviewer is the abstract interface backing every Attractor frontend
// — CLI, web UI, programmatic queue, or remote HTTP.
type Interviewer interface {
	Ask(q Question) (Answer, error)
}

// AutoApprove always answers yes / first option / "auto-approved" text.
// Used in CI and automated tests.
type AutoApprove struct{}

// Ask returns a canonical positive answer.
func (AutoApprove) Ask(q Question) (Answer, error) {
	switch q.Type {
	case QuestionYesNo, QuestionConfirmation:
		return Answer{Value: AnswerYes}, nil
	case QuestionMultipleChoice:
		if len(q.Options) == 0 {
			return Answer{Value: AnswerSkipped}, nil
		}
		opt := q.Options[0]
		return Answer{Value: AnswerChoice, SelectedOption: &opt}, nil
	}
	return Answer{Value: AnswerText, Text: "auto-approved"}, nil
}

// Queue replays answers in FIFO order. Used by deterministic tests.
type Queue struct {
	mu      sync.Mutex
	answers []Answer
}

// NewQueue returns an empty Queue.
func NewQueue(answers ...Answer) *Queue {
	q := &Queue{}
	q.Push(answers...)
	return q
}

// Push appends answers to the end of the queue.
func (q *Queue) Push(answers ...Answer) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.answers = append(q.answers, answers...)
}

// Ask returns the next queued answer; an empty queue yields an error so
// tests do not silently substitute defaults.
func (q *Queue) Ask(_ Question) (Answer, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.answers) == 0 {
		return Answer{}, errors.New("interviewer queue empty")
	}
	a := q.answers[0]
	q.answers = q.answers[1:]
	return a, nil
}
