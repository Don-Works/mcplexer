package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/store"
)

// TODO M3.5: register in gateway/builtin_tools.go alongside
// internal/install.ToolScheduleList/Create/Delete.

// ScheduleListArgs is the decoded argument shape for
// mcplexer__schedule_list. EnabledOnly mirrors the typical UX of "show
// me what's actively scheduled".
type ScheduleListArgs struct {
	EnabledOnly bool   `json:"enabled_only,omitempty"`
	Kind        string `json:"kind,omitempty"`
}

// ScheduleListResult is the response body — bare slice so callers can
// pipe it straight into JSON.
type ScheduleListResult struct {
	Jobs []store.ScheduledJob `json:"jobs"`
}

// ScheduleListHandler returns all scheduled jobs, optionally filtered.
func ScheduleListHandler(
	ctx context.Context, s store.ScheduledJobStore, args ScheduleListArgs,
) (ScheduleListResult, error) {
	jobs, err := s.ListScheduledJobs(ctx)
	if err != nil {
		return ScheduleListResult{}, err
	}
	out := make([]store.ScheduledJob, 0, len(jobs))
	for _, j := range jobs {
		if args.EnabledOnly && !j.Enabled {
			continue
		}
		if args.Kind != "" && j.Kind != args.Kind {
			continue
		}
		out = append(out, j)
	}
	return ScheduleListResult{Jobs: out}, nil
}

// ScheduleCreateArgs is the decoded argument shape for
// mcplexer__schedule_create. ID is optional; when empty a ULID is
// generated.
type ScheduleCreateArgs struct {
	ID                string            `json:"id,omitempty"`
	Name              string            `json:"name"`
	Kind              string            `json:"kind"`
	Spec              string            `json:"spec"`
	Command           string            `json:"command"`
	Args              []string          `json:"args,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	CWD               string            `json:"cwd,omitempty"`
	Enabled           *bool             `json:"enabled,omitempty"`
	SurviveDaemonDown bool              `json:"survive_daemon_down,omitempty"`
}

// ScheduleCreateResult is the success response.
type ScheduleCreateResult struct {
	Job store.ScheduledJob `json:"job"`
}

// ScheduleCreateHandler creates a new scheduled job, validates the
// spec via NextRun, persists the row, and pushes it into the
// scheduler's heap immediately. When SurviveDaemonDown is true and a
// driver is supplied, the native promotion is installed first so a
// half-create (DB written but launchd missing) can't happen.
func ScheduleCreateHandler(
	ctx context.Context,
	s store.ScheduledJobStore,
	sched *Scheduler,
	driver Driver,
	args ScheduleCreateArgs,
) (ScheduleCreateResult, error) {
	if err := validateCreateArgs(args); err != nil {
		return ScheduleCreateResult{}, err
	}
	now := schedulerNow(sched)
	next, err := NextRun(args.Kind, args.Spec, now)
	if err != nil && !errors.Is(err, ErrEventDrivenKind) {
		return ScheduleCreateResult{}, fmt.Errorf("invalid spec: %w", err)
	}
	enabled := true
	if args.Enabled != nil {
		enabled = *args.Enabled
	}
	job := store.ScheduledJob{
		ID:                resolveID(args.ID),
		Name:              args.Name,
		Kind:              args.Kind,
		Spec:              args.Spec,
		Command:           args.Command,
		ArgsJSON:          marshalArgs(args.Args),
		EnvJSON:           marshalEnv(args.Env),
		CWD:               args.CWD,
		Surface:           "schedule",
		Enabled:           enabled,
		SurviveDaemonDown: args.SurviveDaemonDown,
	}
	if !next.IsZero() {
		job.NextRunAt = ptrTime(next)
	}
	if job.SurviveDaemonDown && driver != nil && driver.Available() {
		nativeID, derr := driver.Install(ctx, job)
		if derr != nil {
			return ScheduleCreateResult{}, fmt.Errorf("native promote: %w", derr)
		}
		job.NativeDriver = driver.Name()
		job.NativeID = nativeID
	}
	if err := s.CreateScheduledJob(ctx, &job); err != nil {
		// Roll back the native install so the DB + OS stay aligned.
		if job.NativeID != "" && driver != nil {
			_ = driver.Uninstall(ctx, job.NativeID)
		}
		return ScheduleCreateResult{}, err
	}
	if sched != nil && job.Enabled && job.NextRunAt != nil {
		sched.mu.Lock()
		sched.jobs.upsertByID(job, *job.NextRunAt)
		sched.mu.Unlock()
		sched.kick()
	}
	return ScheduleCreateResult{Job: job}, nil
}

// ScheduleDeleteArgs is the decoded argument shape for
// mcplexer__schedule_delete.
type ScheduleDeleteArgs struct {
	ID string `json:"id"`
}

// ScheduleDeleteResult is the success response.
type ScheduleDeleteResult struct {
	Deleted bool `json:"deleted"`
}

// ScheduleDeleteHandler removes a job by id; uninstalls any native
// promotion via Driver.Uninstall before deleting the row + heap entry.
func ScheduleDeleteHandler(
	ctx context.Context,
	s store.ScheduledJobStore,
	sched *Scheduler,
	driver Driver,
	args ScheduleDeleteArgs,
) (ScheduleDeleteResult, error) {
	if strings.TrimSpace(args.ID) == "" {
		return ScheduleDeleteResult{}, errors.New("id required")
	}
	j, err := s.GetScheduledJob(ctx, args.ID)
	if err != nil {
		return ScheduleDeleteResult{}, err
	}
	if j.NativeID != "" && driver != nil {
		if derr := driver.Uninstall(ctx, j.NativeID); derr != nil {
			return ScheduleDeleteResult{}, fmt.Errorf("native uninstall: %w", derr)
		}
	}
	if err := s.DeleteScheduledJob(ctx, args.ID); err != nil {
		return ScheduleDeleteResult{}, err
	}
	if sched != nil {
		sched.mu.Lock()
		_ = sched.jobs.removeByID(args.ID)
		sched.mu.Unlock()
		sched.kick()
	}
	return ScheduleDeleteResult{Deleted: true}, nil
}

func validateCreateArgs(a ScheduleCreateArgs) error {
	if strings.TrimSpace(a.Name) == "" {
		return errors.New("name required")
	}
	if strings.TrimSpace(a.Kind) == "" {
		return errors.New("kind required")
	}
	if strings.TrimSpace(a.Command) == "" {
		return errors.New("command required")
	}
	return nil
}

func resolveID(id string) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	return "sj-" + ulid.Make().String()
}

// schedulerNow returns the scheduler's clock time when available, or
// the wall clock otherwise. Used by ScheduleCreateHandler when seeding
// the first NextRunAt before the job goes into the heap.
func schedulerNow(s *Scheduler) time.Time {
	if s != nil && s.clock != nil {
		return s.clock.Now()
	}
	return time.Now().UTC()
}

func marshalArgs(a []string) string {
	if len(a) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(a)
	return string(b)
}

func marshalEnv(e map[string]string) string {
	if len(e) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(e)
	return string(b)
}
