// Package sample is a fixture exercising the Go extractor: package doc,
// funcs, methods with pointer and generic receivers, type/interface, and
// const/var blocks.
package sample

import (
	"context"
	"fmt"
)

// MaxItems caps the queue.
const MaxItems = 100

const (
	// StatusOpen is the open state.
	StatusOpen = "open"
	statusDone = "done"
)

// DefaultName is the fallback name.
var DefaultName = "anon"

// Store is a generic keyed store.
type Store[T any] struct {
	items map[string]T
}

// Handler processes requests.
type Handler struct {
	name string
}

// Reader reads values by key.
type Reader interface {
	Read(key string) (string, bool)
}

// HandleKVSet stores a value under key. It reports whether it replaced an
// existing entry.
func HandleKVSet(ctx context.Context, key, value string) (bool, error) {
	return false, nil
}

// Get looks up a value in the generic store.
func (s *Store[T]) Get(key string) (T, bool) {
	v, ok := s.items[key]
	return v, ok
}

// Name returns the handler name.
func (h *Handler) Name() string {
	return h.name
}

// describe is an unexported helper.
func describe(h Handler) string {
	return fmt.Sprintf("handler:%s", h.name)
}
