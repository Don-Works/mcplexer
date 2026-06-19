package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/store"
)

// commandExecutor is the seam that lets tests stub out os/exec without
// also faking the entire approval flow.
type commandExecutor interface {
	Run(ctx context.Context, cmd string, args []string, env []string, cwd string) (stdout, stderr []byte, err error)
}

// osCommandExecutor is the production implementation backed by
// os/exec.CommandContext.
type osCommandExecutor struct{}

func (osCommandExecutor) Run(
	ctx context.Context, cmdName string, args []string, env []string, cwd string,
) ([]byte, []byte, error) {
	c := exec.CommandContext(ctx, cmdName, args...)
	if cwd != "" {
		c.Dir = cwd
	}
	if env != nil {
		c.Env = env
	}
	var outBuf, errBuf cappedBuf
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	err := c.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}

// cappedBuf is an io.Writer that drops bytes after maxOutputBytes.
type cappedBuf struct {
	buf bytes.Buffer
}

func (c *cappedBuf) Write(p []byte) (int, error) {
	remaining := maxOutputBytes - c.buf.Len()
	if remaining <= 0 {
		return len(p), nil // pretend success — caller doesn't need exact bytes
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuf) Bytes() []byte { return c.buf.Bytes() }

var _ io.Writer = (*cappedBuf)(nil)

// executeAndApprove asks the approver, then runs the command. Returns
// the last_status string + the underlying error (if any).
//
// Approval logic:
//   - approver==nil  -> auto-approve (test / headless mode).
//   - approver returns false (denied or timed out) -> status "denied",
//     no exec, no error returned.
//   - approver returns error -> status "failure", surfaced.
func (s *Scheduler) executeAndApprove(
	ctx context.Context, j store.ScheduledJob,
) (string, error) {
	if !j.Enabled {
		return "disabled", nil
	}
	if s.approver != nil {
		approvalCtx, cancel := context.WithTimeout(ctx, approvalTimeoutSec*time.Second)
		defer cancel()
		a := buildScheduleApproval(j)
		ok, err := s.approver.RequestApproval(approvalCtx, a)
		if err != nil {
			return "failure", fmt.Errorf("approval: %w", err)
		}
		if !ok {
			return "denied", nil
		}
	}
	args, err := decodeArgs(j.ArgsJSON)
	if err != nil {
		return "failure", fmt.Errorf("decode args: %w", err)
	}
	env, err := decodeEnv(j.EnvJSON)
	if err != nil {
		return "failure", fmt.Errorf("decode env: %w", err)
	}
	_, _, runErr := s.exec.Run(ctx, j.Command, args, env, j.CWD)
	if runErr != nil {
		return "failure", runErr
	}
	return "success", nil
}

// buildScheduleApproval assembles the approval record for a scheduled
// fire. Surface="schedule" is mandatory; ToolName surfaces the job
// name; Arguments embeds the planned exec invocation as JSON.
func buildScheduleApproval(j store.ScheduledJob) *store.ToolApproval {
	argsPayload := map[string]any{
		"command":  j.Command,
		"args_raw": json.RawMessage(emptyArgsFallback(j.ArgsJSON)),
		"env_raw":  json.RawMessage(emptyEnvFallback(j.EnvJSON)),
		"cwd":      j.CWD,
		"job_id":   j.ID,
		"kind":     j.Kind,
		"spec":     j.Spec,
	}
	raw, _ := json.Marshal(argsPayload)
	return &store.ToolApproval{
		ID:                ulid.Make().String(),
		Status:            "pending",
		Surface:           "schedule",
		ToolName:          "schedule:" + j.Name,
		Arguments:         string(raw),
		Justification:     "scheduled job fire",
		TimeoutSec:        approvalTimeoutSec,
		ApproverType:      "system",
		RequestClientType: "scheduler",
	}
}

func emptyArgsFallback(s string) string {
	if strings.TrimSpace(s) == "" {
		return "[]"
	}
	return s
}

func emptyEnvFallback(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return s
}

// decodeArgs reads a JSON string-array.
func decodeArgs(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// decodeEnv reads a JSON object and returns "K=V" entries. The caller's
// process environment is merged underneath so the child inherits PATH /
// HOME unless explicitly overridden.
func decodeEnv(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var kv map[string]string
	if err := json.Unmarshal([]byte(raw), &kv); err != nil {
		return nil, err
	}
	if len(kv) == 0 {
		return nil, nil
	}
	// Start from the caller's env so PATH/HOME etc. are inherited.
	out := append([]string{}, os.Environ()...)
	for k, v := range kv {
		out = append(out, k+"="+v)
	}
	return out, nil
}

// recordAudit emits an audit row, nil-safe on auditor.
func (s *Scheduler) recordAudit(ctx context.Context, j store.ScheduledJob, status string, runErr error) {
	if s.auditor == nil {
		return
	}
	payload := map[string]string{
		"job_id":  j.ID,
		"command": j.Command,
		"kind":    j.Kind,
		"spec":    j.Spec,
	}
	raw, _ := json.Marshal(payload)
	rec := &store.AuditRecord{
		ID:             ulid.Make().String(),
		Timestamp:      s.clock.Now(),
		ClientType:     "scheduler",
		ToolName:       "schedule:" + j.Name,
		ParamsRedacted: raw,
		Status:         status,
		CreatedAt:      s.clock.Now(),
		ActorKind:      "scheduler",
		ActorID:        j.ID,
	}
	if runErr != nil {
		rec.ErrorMessage = truncateErr(runErr.Error())
	}
	if err := s.auditor.Record(ctx, rec); err != nil {
		// Audit is best-effort; never fail a job because audit didn't.
		_ = err
	}
}
