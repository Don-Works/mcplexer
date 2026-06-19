package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/downstream"
)

type downstreamEventReader interface {
	EventsSince(key downstream.InstanceKey, sinceSeq int64, limit int, methods []string) downstream.EventStreamState
	WaitForEvents(
		ctx context.Context, key downstream.InstanceKey, sinceSeq int64, timeout time.Duration,
		limit int, methods []string,
	) (downstream.EventStreamState, bool)
	EventsBatch(requests []downstream.EventBatchRequest, limit int, methods []string) []downstream.EventStreamState
}

func (h *handler) handleDownstreamEventsSince(_ context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	reader, ok := h.manager.(downstreamEventReader)
	if !ok {
		return marshalToolResult("Downstream event journal is not available."), nil
	}
	var args struct {
		ServerID    string   `json:"server_id"`
		AuthScopeID string   `json:"auth_scope_id"`
		SinceSeq    int64    `json:"since_seq"`
		Limit       int      `json:"limit"`
		Methods     []string `json:"methods"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if args.ServerID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "server_id is required"}
	}
	st := reader.EventsSince(downstream.InstanceKey{
		ServerID: args.ServerID, AuthScopeID: args.AuthScopeID,
	}, args.SinceSeq, args.Limit, args.Methods)
	return marshalDownstreamEventResult(st, false)
}

func (h *handler) handleDownstreamEventsWait(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	reader, ok := h.manager.(downstreamEventReader)
	if !ok {
		return marshalToolResult("Downstream event journal is not available."), nil
	}
	var args struct {
		ServerID       string   `json:"server_id"`
		AuthScopeID    string   `json:"auth_scope_id"`
		SinceSeq       int64    `json:"since_seq"`
		TimeoutSeconds int      `json:"timeout_seconds"`
		Limit          int      `json:"limit"`
		Methods        []string `json:"methods"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if args.ServerID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "server_id is required"}
	}
	timeout := 25 * time.Second
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}
	st, timedOut := reader.WaitForEvents(ctx, downstream.InstanceKey{
		ServerID: args.ServerID, AuthScopeID: args.AuthScopeID,
	}, args.SinceSeq, timeout, args.Limit, args.Methods)
	return marshalDownstreamEventResult(st, timedOut)
}

func (h *handler) handleDownstreamEventsBatch(_ context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	reader, ok := h.manager.(downstreamEventReader)
	if !ok {
		return marshalToolResult("Downstream event journal is not available."), nil
	}
	var args struct {
		Streams []struct {
			ServerID    string `json:"server_id"`
			AuthScopeID string `json:"auth_scope_id"`
			SinceSeq    int64  `json:"since_seq"`
		} `json:"streams"`
		Limit   int      `json:"limit"`
		Methods []string `json:"methods"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if len(args.Streams) == 0 {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "streams is required"}
	}
	reqs := make([]downstream.EventBatchRequest, 0, len(args.Streams))
	for _, s := range args.Streams {
		if s.ServerID == "" {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "each stream requires server_id"}
		}
		reqs = append(reqs, downstream.EventBatchRequest{
			ServerID: s.ServerID, AuthScopeID: s.AuthScopeID, SinceSeq: s.SinceSeq,
		})
	}
	streams := reader.EventsBatch(reqs, args.Limit, args.Methods)
	payload := map[string]any{"streams": streams, "count": len(streams)}
	data, _ := json.Marshal(payload)
	return marshalToolResult(string(data)), nil
}

func marshalDownstreamEventResult(st downstream.EventStreamState, timedOut bool) (json.RawMessage, *RPCError) {
	payload := map[string]any{
		"server_id":     st.ServerID,
		"auth_scope_id": st.AuthScopeID,
		"head_seq":      st.HeadSeq,
		"since_seq":     st.SinceSeq,
		"events":        st.Events,
		"count":         len(st.Events),
		"truncated":     st.Truncated,
		"timed_out":     timedOut,
	}
	if st.Events == nil {
		payload["events"] = []downstream.DownstreamEvent{}
	}
	text, err := json.Marshal(payload)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("marshal events: %v", err)}
	}
	return marshalToolResult(string(text)), nil
}
