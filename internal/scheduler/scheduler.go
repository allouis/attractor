// Package scheduler fires automations on their cron triggers
// (service-spec §5). It is deliberately clock-injectable: Run drives
// Tick from a wall-clock ticker in production, while tests call Tick
// directly with controlled times, so the scheduling logic is verified
// without sleeping. Firing goes through a caller-supplied callback so the
// scheduler stays decoupled from the HTTP server's admission path.
package scheduler

import (
	"log"
	"sync"
	"time"

	"github.com/fabro/attractor/internal/automation"
	"github.com/fabro/attractor/internal/cron"
)

// Fire submits an automation's pipeline, returning the new run id. The
// server wires this to its shared submit path so scheduled and manual
// runs enqueue identically.
type Fire func(a automation.Automation) (string, error)

type item struct {
	auto  automation.Automation
	sched cron.Schedule
	next  time.Time
}

// Scheduler tracks cron-triggered automations and fires them when due.
type Scheduler struct {
	fire Fire
	now  func() time.Time

	mu    sync.Mutex
	items []*item

	stopOnce sync.Once
	quit     chan struct{}
}

// New builds a scheduler over the cron-triggered automations in autos
// (manual-only automations without a cron trigger are skipped). Each
// automation's first fire time is computed relative to now(). Cron specs
// are assumed already validated at load; any that fail to parse are
// dropped defensively.
func New(autos []automation.Automation, fire Fire, now func() time.Time) *Scheduler {
	s := &Scheduler{fire: fire, now: now, quit: make(chan struct{})}
	base := now()
	for _, a := range autos {
		if a.Cron == "" {
			continue
		}
		sched, err := cron.Parse(a.Cron)
		if err != nil {
			continue
		}
		s.items = append(s.items, &item{auto: a, sched: sched, next: sched.Next(base)})
	}
	return s
}

// Tick fires every automation whose next scheduled time is at or before
// now, advancing each to its next occurrence after now. A missed window
// fires once (no catch-up storm). Returns the number fired.
func (s *Scheduler) Tick(now time.Time) int {
	s.mu.Lock()
	var due []automation.Automation
	for _, it := range s.items {
		if it.next.IsZero() || it.next.After(now) {
			continue
		}
		due = append(due, it.auto)
		it.next = it.sched.Next(now)
	}
	s.mu.Unlock()
	for _, a := range due {
		if _, err := s.fire(a); err != nil {
			// Surface a failed submit; dropping it silently would skip a
			// scheduled automation with no trace (no log, event, or retry).
			log.Printf("scheduler: automation %q failed to fire: %v", a.Name, err)
		}
	}
	return len(due)
}

// Run drives Tick from a one-second ticker until Stop. Call it in its
// own goroutine.
func (s *Scheduler) Run() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-ticker.C:
			s.Tick(s.now())
		}
	}
}

// Stop halts the Run loop. Safe to call more than once.
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() { close(s.quit) })
}
