package hexcore

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type DefaultActionDispatcher struct {
	ports  []OutputPort
	fanOut bool
	mu     sync.RWMutex
}

func NewDefaultActionDispatcher() *DefaultActionDispatcher {
	return &DefaultActionDispatcher{}
}

func (d *DefaultActionDispatcher) RegisterOutput(port OutputPort) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ports = append(d.ports, port)
}

func (d *DefaultActionDispatcher) Dispatch(ctx context.Context, action Action) error {
	d.mu.RLock()
	ports := d.ports
	fanOut := d.fanOut
	d.mu.RUnlock()

	var errs []error
	for _, port := range ports {
		if !port.CanDeliver(action) {
			continue
		}
		if err := port.Deliver(ctx, action); err != nil {
			errs = append(errs, fmt.Errorf("deliver to %s: %w", port.Name(), err))
		}
		if !fanOut {
			break
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
