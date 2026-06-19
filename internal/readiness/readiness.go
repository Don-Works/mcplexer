package readiness

import (
	"sync"
)

type State string

const (
	Starting State = "starting"
	Ready    State = "ready"
	Draining State = "draining"
)

type Tracker struct {
	mu    sync.RWMutex
	state State
}

func NewTracker() *Tracker {
	return &Tracker{state: Starting}
}

func (t *Tracker) State() State {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state
}

func (t *Tracker) SetReady() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = Ready
}

func (t *Tracker) SetDraining() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = Draining
}

func (t *Tracker) IsReady() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state == Ready
}

func (t *Tracker) IsDraining() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state == Draining
}
