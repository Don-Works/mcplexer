package lmstudio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MCPServer wraps a Manager as an in-process MCP downstream server.
type MCPServer struct {
	m *Manager
}

// NewMCPServer constructs the wrapper.
func NewMCPServer(m *Manager) *MCPServer { return &MCPServer{m: m} }

// ListTools returns the LM Studio tool surface.
func (s *MCPServer) ListTools(_ context.Context) (json.RawMessage, error) {
	if s.m == nil {
		return json.RawMessage(`{"tools":[]}`), nil
	}
	return json.RawMessage(toolsListJSON), nil
}

// Call dispatches a tool call and returns a CallToolResult-shaped JSON blob.
func (s *MCPServer) Call(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, error) {
	if s.m == nil {
		return errorResult("lmstudio manager not configured"), nil
	}
	if !s.m.Enabled() {
		return errorResult("LM Studio integration is disabled. The operator must set " +
			AllowEnvVar + "=1 in the daemon environment and restart mcplexer. The lms CLI " +
			"spawns host processes with network egress, so it is opt-in like CLI worker providers."), nil
	}
	switch toolName {
	case "status":
		return s.callStatus(ctx)
	case "start_server":
		return wrapText(s.m.StartServer(ctx))
	case "stop_server":
		return wrapText(s.m.StopServer(ctx))
	case "list_models":
		return s.callListModels(ctx)
	case "load_model":
		return s.callWithModel(ctx, args, s.m.LoadModel)
	case "unload_model":
		return s.callWithModel(ctx, args, s.m.UnloadModel)
	case "download_model":
		return s.callWithModel(ctx, args, s.m.DownloadModel)
	default:
		return errorResult(fmt.Sprintf("unknown lmstudio tool %q", toolName)), nil
	}
}

func (s *MCPServer) callStatus(ctx context.Context) (json.RawMessage, error) {
	var b strings.Builder
	if _, err := s.m.resolveBinary(); err != nil {
		fmt.Fprintf(&b, "lms CLI: NOT AVAILABLE (%v)\n", err)
	} else {
		b.WriteString("lms CLI: available\n")
	}
	ids, up, err := s.m.LoadedModels(ctx)
	switch {
	case err != nil:
		fmt.Fprintf(&b, "server at %s: responding but unreadable (%v)\n", s.m.Endpoint(), err)
	case !up:
		fmt.Fprintf(&b, "server at %s: DOWN (use start_server)\n", s.m.Endpoint())
	case len(ids) == 0:
		fmt.Fprintf(&b, "server at %s: UP, no models loaded (use load_model)\n", s.m.Endpoint())
	default:
		fmt.Fprintf(&b, "server at %s: UP, loaded models: %s\n", s.m.Endpoint(), strings.Join(ids, ", "))
	}
	fmt.Fprintf(&b, "delegation hint: model_provider=openai_compat model_endpoint_url=%s/v1 model_id=<loaded model id>", s.m.Endpoint())
	return textResult(b.String()), nil
}

func (s *MCPServer) callListModels(ctx context.Context) (json.RawMessage, error) {
	downloaded, err := s.m.ListDownloaded(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("%v\n%s", err, downloaded)), nil
	}
	loaded, up, _ := s.m.LoadedModels(ctx)
	var b strings.Builder
	b.WriteString("Downloaded models (lms ls):\n")
	b.WriteString(downloaded)
	if up {
		fmt.Fprintf(&b, "\nLoaded into server (%s/v1/models): %s",
			s.m.Endpoint(), strings.Join(loaded, ", "))
		if len(loaded) == 0 {
			b.WriteString("(none)")
		}
	} else {
		fmt.Fprintf(&b, "\nServer at %s is down. Run start_server to load models.", s.m.Endpoint())
	}
	return textResult(b.String()), nil
}

func (s *MCPServer) callWithModel(
	ctx context.Context,
	args json.RawMessage,
	fn func(context.Context, string) (string, error),
) (json.RawMessage, error) {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return errorResult(err.Error()), nil
	}
	if req.Model == "" {
		return errorResult("model is required (an identifier from list_models, e.g. qwen/qwen3-8b)"), nil
	}
	return wrapText(fn(ctx, req.Model))
}

func wrapText(out string, err error) (json.RawMessage, error) {
	if err != nil {
		msg := err.Error()
		if strings.TrimSpace(out) != "" {
			msg += "\n" + out
		}
		return errorResult(msg), nil
	}
	if strings.TrimSpace(out) == "" {
		out = "ok"
	}
	return textResult(out), nil
}

func textResult(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
	return b
}

func errorResult(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": true,
	})
	return b
}
