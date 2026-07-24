package control

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/mcpversion"
	"github.com/don-works/mcplexer/internal/store"
)

// Server is the MCP control server for managing MCPlexer configuration.
type Server struct {
	store    store.Store
	readOnly bool
	mu       sync.Mutex
}

// New creates a new control server.
// When readOnly is true, admin tools (create/update/delete) are blocked.
func New(s store.Store, readOnly ...bool) *Server {
	ro := true // default: read-only for safety
	if len(readOnly) > 0 {
		ro = readOnly[0]
	}
	return &Server{store: s, readOnly: ro}
}

// RunStdio runs the control server over stdio.
func (s *Server) RunStdio(ctx context.Context) error {
	return s.run(ctx, os.Stdin, os.Stdout)
}

func (s *Server) run(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		resp := s.dispatch(ctx, line)
		if resp == nil {
			continue // notification, no response needed
		}

		if err := s.writeResponse(w, resp); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	return scanner.Err()
}

func (s *Server) dispatch(ctx context.Context, line []byte) *gateway.Response {
	var req gateway.Request
	if err := json.Unmarshal(line, &req); err != nil {
		return &gateway.Response{
			JSONRPC: "2.0",
			Error: &gateway.RPCError{
				Code:    gateway.CodeParseError,
				Message: "invalid JSON: " + err.Error(),
			},
		}
	}

	// Notifications have no ID; don't send a response.
	if req.ID == nil {
		s.handleNotification(req)
		return nil
	}

	var result json.RawMessage
	var rpcErr *gateway.RPCError

	switch req.Method {
	case "initialize":
		result, rpcErr = s.handleInitialize(req.Params)
	case "ping":
		result, _ = json.Marshal(map[string]any{})
	case "tools/list":
		result, rpcErr = s.handleToolsList()
	case "tools/call":
		result, rpcErr = s.handleToolsCall(ctx, req.Params)
	default:
		rpcErr = &gateway.RPCError{
			Code:    gateway.CodeMethodNotFound,
			Message: fmt.Sprintf("unknown method: %s", req.Method),
		}
	}

	resp := &gateway.Response{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	return resp
}

func (s *Server) handleNotification(req gateway.Request) {
	switch req.Method {
	case "notifications/initialized":
		slog.Info("control client initialized")
	default:
		slog.Debug("unhandled notification", "method", req.Method)
	}
}

func (s *Server) handleInitialize(
	params json.RawMessage,
) (json.RawMessage, *gateway.RPCError) {
	var request gateway.InitializeParams
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, &gateway.RPCError{
			Code:    gateway.CodeInvalidParams,
			Message: "invalid initialize params: " + err.Error(),
		}
	}
	result := gateway.InitializeResult{
		ProtocolVersion: mcpversion.Select(request.ProtocolVersion),
		Capabilities: gateway.ServerCapability{
			Tools: &gateway.ToolCapability{ListChanged: false},
		},
		ServerInfo: gateway.ServerInfo{
			Name:    "mcplexer-control",
			Version: "0.1.0",
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, &gateway.RPCError{Code: gateway.CodeInternalError, Message: err.Error()}
	}
	return data, nil
}

func (s *Server) handleToolsList() (json.RawMessage, *gateway.RPCError) {
	// The standalone Server dispatches tools/call ONLY via the `handlers`
	// map (handleToolsCall). Tools that the InternalBackend dispatches with
	// extra dependencies (workers, backup, brain, oauth, skill imports,
	// memory import, spawn_subagent) are NOT reachable here and would return
	// CodeMethodNotFound 'unknown tool'. Advertising them would break the
	// tools/list-vs-tools/call contract, so filter allTools() down to the
	// tools this Server can actually dispatch.
	tools := dispatchableTools(allTools())
	if s.readOnly {
		tools = filterReadOnlyTools(tools)
	}
	result := map[string]any{"tools": tools}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, &gateway.RPCError{Code: gateway.CodeInternalError, Message: err.Error()}
	}
	return data, nil
}

// dispatchableTools filters the tool list down to those the standalone
// Server can actually dispatch via the `handlers` map. Anything advertised
// but undispatchable is a broken tools/list-vs-tools/call contract.
func dispatchableTools(tools []gateway.Tool) []gateway.Tool {
	filtered := make([]gateway.Tool, 0, len(tools))
	for _, t := range tools {
		if _, ok := handlers[t.Name]; ok {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// filterReadOnlyTools removes admin tools from the list.
func filterReadOnlyTools(tools []gateway.Tool) []gateway.Tool {
	filtered := make([]gateway.Tool, 0, len(tools))
	for _, t := range tools {
		if !isAdminTool(t.Name) {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

func (s *Server) handleToolsCall(
	ctx context.Context, params json.RawMessage,
) (json.RawMessage, *gateway.RPCError) {
	var req gateway.CallToolRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &gateway.RPCError{
			Code:    gateway.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		}
	}

	handler, ok := handlers[req.Name]
	if !ok {
		return nil, &gateway.RPCError{
			Code:    gateway.CodeMethodNotFound,
			Message: fmt.Sprintf("unknown tool: %s", req.Name),
		}
	}

	if s.readOnly && isAdminTool(req.Name) {
		return nil, &gateway.RPCError{
			Code:    gateway.CodeInvalidRequest,
			Message: fmt.Sprintf("tool %q requires admin access (set MCPLEXER_CONTROL_READONLY=false)", req.Name),
		}
	}

	result, err := handler(ctx, s.store, req.Arguments)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return result, nil
}

func (s *Server) writeResponse(w io.Writer, resp *gateway.Response) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}
