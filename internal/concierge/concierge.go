// Package concierge owns the self-improving cross-channel chat
// subsystem (epic 01KSGKFZMVFZRWVDSZMK8W9JN1). The concierge is a
// worker template that runs as a regular Worker: it consumes inbound
// mesh-triggered turns, emits an assistant reply, then classifies the
// user's next reply to feed the refinement loop.
//
// The package is intentionally thin — most of the heavy lifting lives
// in the worker runner (turn execution), the worker template seed
// (concierge prompt), the skill_refinements subsystem (proposal
// pipeline), and the memory subsystem (lesson pinning). This package
// owns:
//
//   - Classifier — rule-based first pass that maps a user reply onto a
//     ChatTurnLabel (confirmation | correction | frustration | redirect |
//     escalation | neutral). Pluggable so a future model-backed
//     classifier slots in without rewriting callers.
//
//   - Service — thin facade over the ChatTurnSignalStore for the MCP
//     handler + REST API + friction-extractor worker to share.
//
//   - Truncation + sanitisation helpers — keep the stored signals
//     bounded so a noisy channel doesn't bloat the table.
package concierge

import (
	"context"
	"errors"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// maxStoredMessageLen caps how much of the user / assistant message
// we persist per signal. Per-row, not per-conversation — a Telegram
// message can be 4 KB and gchat can be larger. We store enough for
// the friction extractor to read context without bloating the table.
const maxStoredMessageLen = 2 * 1024

// Service is the thin facade callers (MCP handler, REST API, friction
// extractor worker) talk to. Wraps store.ChatTurnSignalStore +
// store.MemoryStore (for lesson pinning under {concierge_lessons}
// scope keys) so callers don't need to thread two store handles.
type Service struct {
	store      ChatTurnSignalStore
	classifier Classifier
}

// ChatTurnSignalStore is the narrow store interface this service needs.
// Lets tests inject a fake without standing up sqlite.
type ChatTurnSignalStore interface {
	InsertChatTurnSignal(ctx context.Context, s *store.ChatTurnSignal) error
	ListChatTurnSignals(ctx context.Context, f store.ChatTurnSignalFilter) ([]store.ChatTurnSignal, error)
	MarkChatTurnSignalPromoted(ctx context.Context, signalID, refinementID string) error
}

// NewService constructs a Service. Pass a nil classifier to use the
// default rule-based one (RuleClassifier).
func NewService(s ChatTurnSignalStore, c Classifier) *Service {
	if c == nil {
		c = NewRuleClassifier()
	}
	return &Service{store: s, classifier: c}
}

// RecordOptions is the arg payload for Record. Mirrors the
// concierge__record_signal tool schema.
type RecordOptions struct {
	WorkerID         string
	WorkspaceID      string
	UserIDExternal   string
	Channel          string
	PromptVersion    int
	TurnID           string
	UserMessage      string
	AssistantMessage string
	SourceSessionID  string

	// Label can be set explicitly when the caller has already classified
	// the turn (e.g. an external classifier or a manual override). When
	// blank, the service's classifier runs.
	Label string
}

// Record classifies the user reply (when Label is blank) and persists
// one ChatTurnSignal row. Returns the persisted row so callers can
// surface the chosen label back to the agent (useful for prompt-
// embedded confidence signalling).
func (s *Service) Record(ctx context.Context, opts RecordOptions) (*store.ChatTurnSignal, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("concierge: not initialised")
	}
	if strings.TrimSpace(opts.WorkerID) == "" {
		return nil, errors.New("concierge: worker_id required")
	}
	if strings.TrimSpace(opts.Channel) == "" {
		return nil, errors.New("concierge: channel required")
	}
	if strings.TrimSpace(opts.UserMessage) == "" {
		return nil, errors.New("concierge: user_message required")
	}

	label := opts.Label
	confidence := 1.0
	classifierKind := store.ChatTurnClassifierRule
	if strings.TrimSpace(label) == "" {
		out := s.classifier.Classify(opts.UserMessage, opts.AssistantMessage)
		label = out.Label
		confidence = out.Confidence
		classifierKind = out.Kind
	}

	row := &store.ChatTurnSignal{
		WorkerID:         opts.WorkerID,
		WorkspaceID:      opts.WorkspaceID,
		UserIDExternal:   opts.UserIDExternal,
		Channel:          opts.Channel,
		PromptVersion:    opts.PromptVersion,
		TurnID:           opts.TurnID,
		Label:            label,
		UserMessage:      truncate(opts.UserMessage),
		AssistantMessage: truncate(opts.AssistantMessage),
		Confidence:       confidence,
		ClassifierKind:   classifierKind,
		SourceSessionID:  opts.SourceSessionID,
	}
	if err := s.store.InsertChatTurnSignal(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// List returns recent signals matching the filter. Used by the friction
// extractor + dashboard friction inbox.
func (s *Service) List(
	ctx context.Context, f store.ChatTurnSignalFilter,
) ([]store.ChatTurnSignal, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("concierge: not initialised")
	}
	return s.store.ListChatTurnSignals(ctx, f)
}

// MarkPromoted records that a signal was fed into a refinement proposal.
// Stops the friction extractor from re-promoting the same negative
// signal on subsequent runs.
func (s *Service) MarkPromoted(
	ctx context.Context, signalID, refinementID string,
) error {
	if s == nil || s.store == nil {
		return errors.New("concierge: not initialised")
	}
	return s.store.MarkChatTurnSignalPromoted(ctx, signalID, refinementID)
}

func truncate(s string) string {
	if len(s) <= maxStoredMessageLen {
		return s
	}
	// Trim to a rune boundary so we don't slice a multibyte character.
	cut := maxStoredMessageLen
	for cut > 0 && cut < len(s) {
		b := s[cut]
		if b&0xC0 != 0x80 {
			break
		}
		cut--
	}
	return s[:cut] + "…"
}
