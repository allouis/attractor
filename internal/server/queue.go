package server

import "sync"

// dispatcher admits queued runs into a bounded set of execution slots,
// FIFO (service-spec §3). Submissions append to the queue and return
// immediately; a single dispatch goroutine pops the head, blocks until a
// slot frees, and launches the run — so runs start in submission order
// and never exceed the concurrency limit.
type dispatcher struct {
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []*Run
	slots  chan struct{}
	quit   chan struct{}
	closed bool
	// launch executes a dispatched run to completion (blocking so the slot
	// is held for the run's duration). The server sets it to route through
	// the configured Launcher; nil falls back to the in-process path.
	launch func(*Run)
}

func newDispatcher(max int) *dispatcher {
	if max <= 0 {
		max = defaultMaxConcurrentRuns
	}
	d := &dispatcher{
		slots: make(chan struct{}, max),
		quit:  make(chan struct{}),
	}
	d.cond = sync.NewCond(&d.mu)
	return d
}

// enqueue appends a run to the FIFO queue and wakes the dispatch loop.
func (d *dispatcher) enqueue(r *Run) {
	d.mu.Lock()
	d.queue = append(d.queue, r)
	d.mu.Unlock()
	d.cond.Signal()
}

// run is the dispatch loop; call it in its own goroutine.
func (d *dispatcher) run() {
	for {
		next := d.dequeue()
		if next == nil {
			return // closed
		}
		// A run cancelled while queued is dropped without a slot.
		if next.IsCancelled() {
			continue
		}
		select {
		case d.slots <- struct{}{}:
		case <-d.quit:
			return
		}
		// Re-check: it may have been cancelled while we waited for a slot.
		if next.IsCancelled() {
			<-d.slots
			continue
		}
		go func(r *Run) {
			defer func() { <-d.slots }()
			if d.launch != nil {
				d.launch(r)
				return
			}
			r.execute()
		}(next)
	}
}

// dequeue blocks until a run is available and returns the FIFO head, or
// nil once the dispatcher is closed.
func (d *dispatcher) dequeue() *Run {
	d.mu.Lock()
	defer d.mu.Unlock()
	for len(d.queue) == 0 && !d.closed {
		d.cond.Wait()
	}
	if d.closed {
		return nil
	}
	r := d.queue[0]
	d.queue = d.queue[1:]
	return r
}

// close stops the dispatch loop. In-flight runs are left to finish.
func (d *dispatcher) close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.mu.Unlock()
	close(d.quit)
	d.cond.Broadcast()
}
