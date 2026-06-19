package downstream

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type startCall struct {
	done chan struct{}
	inst downstream
	err  error
}

func (m *Manager) getOrStart(ctx context.Context, key InstanceKey) (downstream, error) {
	keyLock := m.lockForKey(key)
	keyLock.Lock()
	defer keyLock.Unlock()

	m.mu.Lock()
	if inst, ok := m.instances[key]; ok {
		state := inst.getState()
		if state == StateRestarting {
			m.mu.Unlock()
			select {
			case <-inst.waitRestartDone():
				state = inst.getState()
				if state != StateStopped {
					return inst, nil
				}
				m.mu.Lock()
				delete(m.instances, key)
				m.mu.Unlock()
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else if state != StateStopped {
			m.mu.Unlock()
			return inst, nil
		} else {
			delete(m.instances, key)
			m.mu.Unlock()
		}
	} else {
		m.mu.Unlock()
	}

	call, leader := m.beginStart(key)
	if !leader {
		select {
		case <-call.done:
			return call.inst, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Per-session keys belong to browser-automation servers
	// (ShouldIsolatePerSession), which spawn one stateful process per logical
	// agent. Cap the per-server total before spawning another so a busy
	// machine with many live sessions + workers can't accumulate unbounded
	// headless Chromium processes — evicting the oldest session's browser
	// keeps us within the bound. No-op for the shared (empty-session)
	// lifecycle and for servers without a cap.
	if key.SessionID != "" {
		if max := m.capForServer(ctx, key.ServerID); max > 0 {
			m.enforceInstanceCap(key, max)
		}
	}

	inst, err := m.createInstance(ctx, key)
	if err != nil {
		m.finishStart(key, call, nil, err)
		return nil, err
	}

	if err := inst.start(ctx); err != nil {
		err = fmt.Errorf("start instance: %w", err)
		m.finishStart(key, call, nil, err)
		return nil, err
	}

	m.mu.Lock()
	m.instances[key] = inst
	m.instanceStartedAt[key] = time.Now()
	m.mu.Unlock()

	m.finishStart(key, call, inst, nil)
	return inst, nil
}

// capForServer resolves the per-server concurrent-instance cap for a browser-
// class server. Loads the row once; on lookup failure returns 0 (no cap) so a
// transient store error never blocks a spawn.
func (m *Manager) capForServer(ctx context.Context, serverID string) int {
	srv, err := m.store.GetDownstreamServer(ctx, serverID)
	if err != nil || srv == nil {
		return 0
	}
	return maxInstancesForServer(srv)
}

func (m *Manager) lockForKey(key InstanceKey) *sync.Mutex {
	m.keyMu.Lock()
	defer m.keyMu.Unlock()
	if mu, ok := m.keyMutexes[key]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	m.keyMutexes[key] = mu
	return mu
}

func (m *Manager) beginStart(key InstanceKey) (*startCall, bool) {
	m.startFlightMu.Lock()
	defer m.startFlightMu.Unlock()
	if call, ok := m.startFlight[key]; ok {
		return call, false
	}
	call := &startCall{done: make(chan struct{})}
	m.startFlight[key] = call
	return call, true
}

func (m *Manager) finishStart(key InstanceKey, call *startCall, inst downstream, err error) {
	m.startFlightMu.Lock()
	if m.startFlight[key] == call {
		delete(m.startFlight, key)
	}
	call.inst = inst
	call.err = err
	close(call.done)
	m.startFlightMu.Unlock()
}
