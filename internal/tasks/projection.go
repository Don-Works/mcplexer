package tasks

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

const (
	EgressProfileTaskSafeV1   = "task-safe-v1"
	EgressProfileLogSummaryV1 = "log-summary-v1"

	maxProjectedTitleBytes       = 512
	maxProjectedDescriptionBytes = 32 * 1024
	maxProjectedTags             = 32
	maxProjectedTagBytes         = 128
)

var ErrUnknownEgressProfile = errors.New("tasks: unknown egress profile")

// BuildSafeLocalEventForGossip creates the only task projection permitted on
// collaboration streams. It excludes opaque meta, local session/peer/user IDs,
// notes, attachments, evidence, and assignment internals; free text and tags
// pass through the canonical credential-pattern redactor and hard size caps.
func BuildSafeLocalEventForGossip(t *store.Task, selfPeerID, profile string) (p2p.TaskSyncEvent, error) {
	patch, err := safeRemoteTaskPatch(t, profile)
	if err != nil {
		return p2p.TaskSyncEvent{}, err
	}
	raw, err := json.Marshal(&patch)
	if err != nil {
		return p2p.TaskSyncEvent{}, err
	}
	return p2p.TaskSyncEvent{
		Type: "task_event", TaskID: t.ID, WorkspaceID: t.WorkspaceID,
		HLC: t.HlcAt, ByPeer: selfPeerID, FieldPatchesJSON: raw,
	}, nil
}

func BuildSafeTaskPayload(t *store.Task, profile string) (*p2p.TaskPayloadEnvelope, error) {
	patch, err := safeRemoteTaskPatch(t, profile)
	if err != nil {
		return nil, err
	}
	return &p2p.TaskPayloadEnvelope{
		EnvelopeKind: p2p.TaskEnvelopeKindPayload, RemoteTaskID: t.ID,
		BaseHLC: t.RemoteBaseHLC,
		Title:   patch.Title, Description: patch.Description,
		Status: patch.Status, Priority: patch.Priority, DueAt: patch.DueAt,
		Tags: patch.TagsJSON,
	}, nil
}

func sanitizeIncomingTaskOffer(envelope *p2p.TaskOfferEnvelope) {
	if envelope == nil {
		return
	}
	envelope.Title = safeProjectedText(envelope.Title, maxProjectedTitleBytes)
	envelope.DescriptionPreview = safeProjectedText(envelope.DescriptionPreview, taskOfferPreviewCap)
	envelope.MetaPreview = ""
	envelope.StatusPreview = safeProjectedText(envelope.StatusPreview, 128)
	envelope.PriorityPreview = safeProjectedText(envelope.PriorityPreview, 128)
	envelope.Tags = safeProjectedTags(envelope.Tags)
	envelope.Message = safeProjectedText(envelope.Message, taskOfferPreviewCap)
}

func sanitizeIncomingTaskPayload(payload *p2p.TaskPayloadEnvelope) {
	if payload == nil {
		return
	}
	payload.Title = safeProjectedText(payload.Title, maxProjectedTitleBytes)
	payload.Description = safeProjectedText(payload.Description, maxProjectedDescriptionBytes)
	payload.Status = safeProjectedText(payload.Status, 128)
	payload.Priority = safeProjectedText(payload.Priority, 128)
	payload.Meta = ""
	payload.Tags = safeProjectedTags(payload.Tags)
}

func safeRemoteTaskPatch(t *store.Task, profile string) (RemoteTaskPatch, error) {
	if t == nil {
		return RemoteTaskPatch{}, errors.New("tasks: task is required")
	}
	switch profile {
	case EgressProfileTaskSafeV1, EgressProfileLogSummaryV1:
	default:
		return RemoteTaskPatch{}, ErrUnknownEgressProfile
	}
	return RemoteTaskPatch{
		Title:       safeProjectedText(t.Title, maxProjectedTitleBytes),
		Description: safeProjectedText(t.Description, maxProjectedDescriptionBytes),
		Status:      safeProjectedText(t.Status, 128),
		Priority:    safeProjectedText(t.Priority, 128),
		TagsJSON:    safeProjectedTags(t.TagsJSON),
		DueAt:       t.DueAt,
		ClosedAt:    t.ClosedAt,
	}, nil
}

func safeProjectedText(value string, limit int) string {
	value = audit.RedactString(value, nil)
	value = strings.ReplaceAll(value, "\x00", "")
	return truncate(value, limit)
}

func safeProjectedTags(raw json.RawMessage) json.RawMessage {
	var tags []string
	if err := json.Unmarshal(raw, &tags); err != nil {
		return json.RawMessage(`[]`)
	}
	if len(tags) > maxProjectedTags {
		tags = tags[:maxProjectedTags]
	}
	for i := range tags {
		tags[i] = safeProjectedText(tags[i], maxProjectedTagBytes)
	}
	result, err := json.Marshal(tags)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return result
}
