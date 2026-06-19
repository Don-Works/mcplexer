package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// outputChannel is one entry in the Worker.OutputChannelsJSON array.
// Channel-specific fields are intentionally a wide union — every type
// only consumes the subset it needs and ignores the rest. JSON tags on
// each field keep the wire format identical to the original M0 shape.
type outputChannel struct {
	Type     string `json:"type"`
	Priority string `json:"priority,omitempty"`
	// PriorityOnFail (mesh channels) overrides Priority when the run's
	// status is not StatusSuccess. Empty → Priority applies regardless of
	// status. Lets a template emit at "low" on a green run and "high" on
	// a red one without declaring two channel entries.
	PriorityOnFail string            `json:"priority_on_fail,omitempty"`
	Tags           string            `json:"tags,omitempty"`
	Path           string            `json:"path,omitempty"`
	Mode           string            `json:"mode,omitempty"` // "append" | "overwrite"
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	// NotifyUser=true on a mesh channel fires a notify-bus event so the
	// telegram bridge + PWA toast pick it up. Required for telegram
	// responder workers to actually reach the user.
	NotifyUser bool `json:"notify_user,omitempty"`
	// ReplyToTrigger=true threads the emission as reply_to=the run's
	// triggering mesh message — telegram + UI render it as a reply.
	// Combined with NotifyUser this is the "respond to the message
	// that fired me" pattern.
	ReplyToTrigger bool `json:"reply_to_trigger,omitempty"`
	// ToPeer deliberately routes a mesh output channel to one paired
	// device. Empty keeps worker output local unless BroadcastPeers is true.
	ToPeer string `json:"to_peer,omitempty"`
	// BroadcastPeers deliberately fans a mesh output channel out to paired
	// devices. Default false prevents schedule/manual worker output from
	// leaking to every paired machine just because its local audience is "*".
	BroadcastPeers bool `json:"broadcast_peers,omitempty"`
	// IncludeMetadata=true (webhook) attaches the run's full envelope
	// (worker id, run id, tokens, cost) alongside the output text. When
	// false the body is the bare {"output": "..."} shape.
	IncludeMetadata bool   `json:"include_metadata,omitempty"`
	Channel         string `json:"channel,omitempty"`         // slack #channel hint
	Prefix          string `json:"prefix,omitempty"`          // slack / clickup / github prefix
	ListID          string `json:"list_id,omitempty"`         // clickup list
	NamePrefix      string `json:"name_prefix,omitempty"`     // clickup name prefix
	Repo            string `json:"repo,omitempty"`            // github owner/repo
	TitlePrefix     string `json:"title_prefix,omitempty"`    // github issue title prefix
	SecretScopeID   string `json:"secret_scope_id,omitempty"` // clickup / github API token scope
}

// outputContext bundles every field a channel implementation might want:
// the run-shaped metadata (for webhook bodies + alert messages) plus the
// HTTP / secret collaborators. Constructed once per finalize() call so
// individual channel funcs stay parameter-light.
type outputContext struct {
	workerID     string
	workerName   string
	workspaceID  string
	runID        string
	status       string
	output       string
	startedAt    time.Time
	finishedAt   time.Time
	durationMS   int64
	inputTokens  int
	outputTokens int
	costUSD      float64
	httpClient   *http.Client
	secrets      SecretReader
	mesh         MeshSender
	// chainDepth is the inbound trigger depth for this run. Used by the
	// mesh output emitter to stamp "chain-depth:N+1" so downstream
	// dispatchers see the increment and the loop guard holds. 0 for
	// schedule-driven runs (their emissions still land at depth 1).
	chainDepth int
	// triggerMessageID is the source mesh message that fired this run,
	// when TriggerKind="mesh". Used by emitMeshOutput when an output
	// channel sets reply_to_trigger=true.
	triggerMessageID string
	// agentDisplayName is the human-friendly attribution label for this
	// worker's mesh emissions. Populated at run-start so we can render
	// e.g. "telegram-responder [Telegram, opencode_cli:MiniMax-M3]"
	// instead of the generic "worker". Empty falls back to the legacy
	// behaviour.
	agentDisplayName string
}

// hasMeshOutputChannel returns true when the worker's output channel
// list contains a mesh entry. Used by finalize to suppress the
// duplicate summary on the worker.finished lifecycle signal when the
// mesh-output emission already carries the same text. Parse failures
// return false (fail-quiet: the worst case is the legacy duplicate
// behaviour, which is what we're already shipping).
func hasMeshOutputChannel(channelsJSON string) bool {
	if channelsJSON == "" {
		return false
	}
	channels, err := parseOutputChannels(channelsJSON)
	if err != nil {
		return false
	}
	for _, ch := range channels {
		if strings.EqualFold(ch.Type, "mesh") {
			return true
		}
	}
	return false
}

// emitOutputs fans the run's final output text out to every configured
// channel. Channel-level errors are logged AND surface as a mesh alert
// (when mesh is wired) but never abort the emission — best-effort
// delivery so one broken sink can't drop the whole result. Returns the
// set of mesh message IDs created (output_mesh + any error alerts).
// emptyReplyFallbackText resolves a conversational worker's pending
// "thinking" placeholder when the model produced no usable text — so the
// user sees a clear message instead of a bubble that updates to nothing.
const emptyReplyFallbackText = "⚠️ I couldn't generate a reply just now — please try again in a moment."

// replyTriggerMeshChannels returns the mesh channels that thread their
// output back to the originating trigger (reply_to_trigger=true) — i.e.
// conversational reply channels like the Telegram concierge. Used to
// resolve a stuck placeholder when the model produced no final text.
func replyTriggerMeshChannels(channels []outputChannel) []outputChannel {
	var out []outputChannel
	for _, ch := range channels {
		if strings.EqualFold(ch.Type, "mesh") && ch.ReplyToTrigger {
			out = append(out, ch)
		}
	}
	return out
}

func (r *Runner) emitOutputs(ctx context.Context, octx outputContext, channelsJSON string) []string {
	if channelsJSON == "" {
		return nil
	}
	channels, err := parseOutputChannels(channelsJSON)
	if err != nil {
		slog.Warn("worker output channels: parse",
			"worker_id", octx.workerID,
			"run_id", octx.runID,
			"error", err,
		)
		return nil
	}
	// Whitespace-only model output is "no output" — trim once here so
	// every channel sink (mesh finding, file, webhook, …) sees the
	// trimmed text and a blank-but-not-empty reply still resolves a
	// pending conversational placeholder instead of posting whitespace.
	octx.output = strings.TrimSpace(octx.output)
	if octx.output == "" {
		// The model produced no final text (e.g. an opencode stream
		// truncation that even the adapter's retry couldn't recover). For a
		// conversational reply channel (mesh + reply_to_trigger, e.g. the
		// Telegram concierge) we MUST still resolve the user's in-flight
		// "thinking" placeholder, so substitute a visible fallback and
		// dispatch ONLY those channels. Other sinks (file/webhook/clickup/
		// github) stay silent on empty output — no junk artifacts.
		// Only resolve a placeholder when there's a real message to reply
		// to: a reply_to_trigger mesh channel AND a triggering message id.
		// Manual run-now / schedule runs (no trigger) stay silent so we
		// don't post spurious "couldn't reply" notices to the chat.
		reply := replyTriggerMeshChannels(channels)
		if len(reply) == 0 || octx.triggerMessageID == "" {
			return nil
		}
		slog.Warn("worker output: empty model text — emitting reply fallback",
			"worker_id", octx.workerID,
			"run_id", octx.runID,
		)
		octx.output = emptyReplyFallbackText
		channels = reply
	}
	var meshIDs []string
	for _, ch := range channels {
		id := r.dispatchChannel(ctx, octx, ch)
		if id != "" {
			meshIDs = append(meshIDs, id)
		}
	}
	return meshIDs
}

// dispatchChannel routes one outputChannel to its implementation. On
// failure (every non-mesh channel reports the error back, which we
// surface as a high-priority mesh alert) returns an empty mesh ID
// because the success path here is "delivery happened, no need to
// echo into the mesh ledger". The mesh channel itself returns its
// own emitted ID so the run row carries the cross-reference.
//
// Every channel emission produces a worker_output.emitted audit
// record (success or failure) so the audit ledger captures
// every external-facing side effect.
func (r *Runner) dispatchChannel(ctx context.Context, octx outputContext, ch outputChannel) string {
	started := r.clock.Now()
	var (
		meshID  string
		emitErr error
	)
	switch ch.Type {
	case "mesh":
		meshID = r.emitMeshOutput(ctx, octx, ch)
	case "file":
		emitErr = writeFileOutput(ch, octx.output, r.outputsDir)
	case "webhook":
		emitErr = emitWebhookOutput(ctx, octx, ch)
	case "slack_webhook":
		emitErr = emitSlackWebhookOutput(ctx, octx, ch)
	case "clickup_task":
		emitErr = emitClickUpTaskOutput(ctx, octx, ch)
	case "github_issue":
		emitErr = emitGitHubIssueOutput(ctx, octx, ch)
	default:
		slog.Warn("worker output channel: unsupported type",
			"worker_id", octx.workerID, "run_id", octx.runID, "type", ch.Type)
		return ""
	}
	if emitErr != nil {
		r.reportChannelError(ctx, octx, ch.Type, emitErr)
	}
	r.recordOutputAudit(ctx, octx, ch, started, emitErr)
	return meshID
}

// reportChannelError logs the failure and emits a high-priority mesh
// alert so operators see the failure even when the channel itself was
// the only place the result was supposed to land.
func (r *Runner) reportChannelError(ctx context.Context, octx outputContext, channelType string, err error) {
	slog.Warn("worker output channel: emit failed",
		"worker_id", octx.workerID,
		"run_id", octx.runID,
		"channel", channelType,
		"error", err,
	)
	if octx.mesh == nil {
		return
	}
	r.emitSignal(ctx, octx.workerID, octx.runID, MeshOutbound{
		Kind:     "alert",
		Priority: "high",
		Tags:     "worker,output," + channelType + ",error",
		Content:  fmt.Sprintf("worker %s output channel %q failed: %v", octx.workerName, channelType, err),
	})
}

// parseOutputChannels decodes the OutputChannelsJSON array. Empty or
// "null" input → empty slice (no error).
func parseOutputChannels(channelsJSON string) ([]outputChannel, error) {
	if channelsJSON == "" || channelsJSON == "null" {
		return nil, nil
	}
	var channels []outputChannel
	if err := json.Unmarshal([]byte(channelsJSON), &channels); err != nil {
		return nil, err
	}
	return channels, nil
}

// emitMeshOutput posts the run's output as a finding-kind mesh message
// so operators see the substantive result alongside the worker.finished
// lifecycle event. Returns the new message id (or empty on failure).
//
// When this run was itself triggered by a mesh message (octx.chainDepth
// > 0), we stamp a "chain-depth:N+1" tag on the output so the next
// layer's trigger dispatcher sees the increment and the loop guard
// holds. Schedule-driven runs (chainDepth == 0) emit at depth 1 so a
// downstream listener's MaxChainDepth=3 still trips on the third
// reflexive hop.
func (r *Runner) emitMeshOutput(ctx context.Context, octx outputContext, ch outputChannel) string {
	if r.mesh == nil {
		return ""
	}
	priority := resolveMeshPriority(ch, octx.status)
	tags := "worker,output"
	if ch.Tags != "" {
		tags = tags + "," + ch.Tags
	}
	if depth := octx.chainDepth; depth >= 0 {
		// Always advance the chain depth by 1 — emissions from schedule
		// runs land at 1; emissions from a mesh-triggered run at depth N
		// land at N+1. Trigger.MaxChainDepth controls re-entry.
		tags += ",chain-depth:" + formatInt(depth+1)
	}
	out := MeshOutbound{
		Kind:             "finding",
		Priority:         priority,
		Tags:             tags,
		Content:          octx.output,
		WorkspaceID:      octx.workspaceID,
		NotifyUser:       ch.NotifyUser,
		ToPeer:           ch.ToPeer,
		BroadcastPeers:   ch.BroadcastPeers,
		AgentDisplayName: octx.agentDisplayName,
	}
	if ch.ReplyToTrigger {
		out.ReplyTo = octx.triggerMessageID
	}
	return r.emitSignal(ctx, octx.workerID, octx.runID, out)
}

// resolveMeshPriority picks the broadcast priority for one mesh output
// emission. Default behaviour: use ch.Priority verbatim (falling back to
// "normal" when unset). When the run terminated in a non-success state
// AND the channel declared a `priority_on_fail`, that override wins.
// Empty `priority_on_fail` preserves the legacy behaviour — the static
// `priority` value applies regardless of run status, so every template
// that doesn't opt in keeps shipping the exact same wire shape.
func resolveMeshPriority(ch outputChannel, status string) string {
	if status != "" && status != StatusSuccess && ch.PriorityOnFail != "" {
		return ch.PriorityOnFail
	}
	if ch.Priority == "" {
		return "normal"
	}
	return ch.Priority
}

// writeFileOutput writes output to a path under the worker-outputs root.
// Mode "overwrite" truncates; every other value (including the default
// and "append") appends.
//
// SECURITY: ch.Path is jailed to outputsRoot via resolveOutputPath.
// Paths that escape the root (via "..", absolute paths, symlinks
// rooted elsewhere) are rejected. An empty outputsRoot disables the
// file channel entirely — operators who configured it pre-jail will
// see an explicit error rather than silent disk writes.
func writeFileOutput(ch outputChannel, output, outputsRoot string) error {
	if ch.Path == "" {
		return fmt.Errorf("file channel: empty path")
	}
	if outputsRoot == "" {
		return fmt.Errorf("file channel: outputs root not configured")
	}
	resolved, err := resolveOutputPath(outputsRoot, ch.Path)
	if err != nil {
		return fmt.Errorf("file channel: %w", err)
	}
	if dir := filepath.Dir(resolved); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	flag := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if ch.Mode == "overwrite" {
		flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(resolved, flag, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", resolved, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(output); err != nil {
		return fmt.Errorf("write %s: %w", resolved, err)
	}
	if ch.Mode != "overwrite" {
		// Trailing newline so successive appends don't run together.
		_, _ = f.WriteString("\n")
	}
	return nil
}

// resolveOutputPath returns the absolute on-disk path for a configured
// file-channel target, after enforcing that the result lives under
// outputsRoot. Returns an error when the user-supplied path escapes
// (via "..", absolute prefix, or symlink-rooted detour). The check uses
// filepath.Rel + a "../" prefix scan rather than HasPrefix on the
// joined path so OS-specific path separators don't break the guard.
func resolveOutputPath(outputsRoot, userPath string) (string, error) {
	absRoot, err := filepath.Abs(outputsRoot)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	cleanRoot := filepath.Clean(absRoot)
	// Treat user paths as relative under the root. Absolute paths are
	// allowed only when they're already inside the root.
	target := userPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(cleanRoot, target)
	}
	target = filepath.Clean(target)
	rel, err := filepath.Rel(cleanRoot, target)
	if err != nil {
		return "", fmt.Errorf("path escapes outputs root: %s", userPath)
	}
	if rel == ".." || rel == "." || rel == "" {
		// "." is valid (root itself, but file channels need a file name);
		// ".." means escape. Treat both as errors here.
		if rel == ".." {
			return "", fmt.Errorf("path escapes outputs root: %s", userPath)
		}
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("path escapes outputs root: %s", userPath)
	}
	return target, nil
}
