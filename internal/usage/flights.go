package usage

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/clistats"
)

type providerFlight struct {
	done     chan struct{}
	snapshot store.ProviderSnapshot
}

type openRouterFlight struct {
	done     chan struct{}
	snapshot store.OpenRouterSnapshot
}

type localStatsFlight struct {
	done  chan struct{}
	stats []clistats.ModelStats
	err   error
}

func (s *Service) beginProviderFlight(key string) (*providerFlight, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if flight := s.providerFlights[key]; flight != nil {
		return flight, false
	}
	if s.providerFlights == nil {
		s.providerFlights = make(map[string]*providerFlight)
	}
	flight := &providerFlight{done: make(chan struct{})}
	s.providerFlights[key] = flight
	return flight, true
}

func (s *Service) finishProviderFlight(
	key string,
	flight *providerFlight,
	snapshot store.ProviderSnapshot,
) {
	s.mu.Lock()
	flight.snapshot = snapshot
	delete(s.providerFlights, key)
	close(flight.done)
	s.mu.Unlock()
}

func waitProviderFlight(
	ctx context.Context,
	flight *providerFlight,
	cfg store.SourceConfig,
) store.ProviderSnapshot {
	select {
	case <-ctx.Done():
		return providerError(cfg, store.StatusError, ctx.Err().Error())
	case <-flight.done:
		return flight.snapshot
	}
}

func (s *Service) beginOpenRouterFlight(key string) (*openRouterFlight, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if flight := s.orFlights[key]; flight != nil {
		return flight, false
	}
	if s.orFlights == nil {
		s.orFlights = make(map[string]*openRouterFlight)
	}
	flight := &openRouterFlight{done: make(chan struct{})}
	s.orFlights[key] = flight
	return flight, true
}

func (s *Service) finishOpenRouterFlight(
	key string,
	flight *openRouterFlight,
	snapshot store.OpenRouterSnapshot,
) {
	s.mu.Lock()
	flight.snapshot = snapshot
	delete(s.orFlights, key)
	close(flight.done)
	s.mu.Unlock()
}

func waitOpenRouterFlight(
	ctx context.Context,
	flight *openRouterFlight,
) store.OpenRouterSnapshot {
	select {
	case <-ctx.Done():
		return store.OpenRouterSnapshot{Status: store.StatusError, Error: ctx.Err().Error()}
	case <-flight.done:
		return flight.snapshot
	}
}

func (s *Service) beginLocalStatsFlight(key string) (*localStatsFlight, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if flight := s.localFlights[key]; flight != nil {
		return flight, false
	}
	if s.localFlights == nil {
		s.localFlights = make(map[string]*localStatsFlight)
	}
	flight := &localStatsFlight{done: make(chan struct{})}
	s.localFlights[key] = flight
	return flight, true
}

func (s *Service) finishLocalStatsFlight(
	key string,
	flight *localStatsFlight,
	stats []clistats.ModelStats,
	err error,
) {
	s.mu.Lock()
	flight.stats, flight.err = stats, err
	delete(s.localFlights, key)
	close(flight.done)
	s.mu.Unlock()
}

func waitLocalStatsFlight(
	ctx context.Context,
	flight *localStatsFlight,
) ([]clistats.ModelStats, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return flight.stats, flight.err
	}
}
