package downstream

import (
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/mcpversion"
)

type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
}

func newInitializeRequest() jsonRPCRequest {
	return jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params: json.RawMessage(
			`{"protocolVersion":"` + mcpversion.Latest +
				`","capabilities":{},"clientInfo":{"name":"mcplexer","version":"0.1.0"}}`,
		),
	}
}

func validateInitializeResult(result json.RawMessage) (string, error) {
	var selected initializeResult
	if err := json.Unmarshal(result, &selected); err != nil {
		return "", fmt.Errorf("decode initialize result: %w", err)
	}
	if err := mcpversion.ValidateSelected(selected.ProtocolVersion); err != nil {
		return "", err
	}
	return selected.ProtocolVersion, nil
}
