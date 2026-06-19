// health.go — degraded-mode boot probe for the tasks subsystem.
// A database whose schema_version got bumped past the tasks migrations
// without the ALTERs actually applying (branch swaps, partial restores)
// makes every task__* call fail with an opaque "SQL logic error". The
// probe runs one cheap SELECT at service construction touching the
// late-migration columns the service depends on; on failure the
// service flags itself degraded and the gateway surfaces an actionable
// message instead of the raw driver error.
package tasks

import (
	"context"
	"fmt"
	"log/slog"
)

// taskSchemaProber is the optional store capability the probe uses.
// Implemented by the sqlite store (ProbeTaskSchema); test fakes that
// don't implement it skip the probe entirely.
type taskSchemaProber interface {
	ProbeTaskSchema(ctx context.Context) error
}

// probeSchema runs the boot probe and records the failure on the
// service. Called once from New; the flag is written before the
// service is shared, so reads need no lock.
func (s *Service) probeSchema(ctx context.Context) {
	p, ok := s.store.(taskSchemaProber)
	if !ok {
		return
	}
	if err := p.ProbeTaskSchema(ctx); err != nil {
		s.schemaErr = fmt.Errorf("tasks schema probe failed: %w", err)
		slog.Error("tasks: schema probe failed — task__* tools degraded until migrations apply",
			"err", err,
			"remedy", "restart the mcplexer daemon to re-run migrations (or `mcplexer upgrade`)")
	}
}

// SchemaErr returns the boot-probe failure, or nil when the tasks
// schema is healthy. The gateway checks this before dispatching any
// task__* tool so callers get an actionable error string.
func (s *Service) SchemaErr() error {
	return s.schemaErr
}
