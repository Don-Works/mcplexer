package hexcore

import (
	"context"
	"log"
	"sync"
)

type DefaultEventRouter struct {
	handlers map[string]EventHandler
	mu       sync.RWMutex
}

func NewDefaultEventRouter() *DefaultEventRouter {
	return &DefaultEventRouter{
		handlers: make(map[string]EventHandler),
	}
}

func (r *DefaultEventRouter) RegisterHandler(kind string, handler EventHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[kind] = handler
}

func (r *DefaultEventRouter) Route(ctx context.Context, event Event) error {
	r.mu.RLock()
	handler, ok := r.handlers[event.Kind]
	r.mu.RUnlock()

	if !ok {
		log.Printf("hexcore: unhandled event kind %q from %s", event.Kind, event.Source)
		return nil
	}
	return handler(ctx, event)
}
