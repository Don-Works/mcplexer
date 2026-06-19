package models

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type mimoCLIErrorEvent struct {
	Type  string `json:"type"`
	Error struct {
		Name    string `json:"name"`
		Message string `json:"message"`
		Data    struct {
			Message    string `json:"message"`
			StatusCode int    `json:"statusCode"`
			Metadata   struct {
				URL string `json:"url"`
			} `json:"metadata"`
		} `json:"data"`
	} `json:"error"`
}

func parseMimoJSON(raw []byte) (*SendResponse, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev mimoCLIErrorEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Type == "error" {
			return nil, fmt.Errorf("mimo_cli error event: %s", formatMimoErrorEvent(ev))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return parseOpenCodeNDJSON(raw)
}

func formatMimoErrorEvent(ev mimoCLIErrorEvent) string {
	parts := make([]string, 0, 3)
	if ev.Error.Name != "" {
		parts = append(parts, ev.Error.Name)
	}
	msg := ev.Error.Message
	if msg == "" {
		msg = ev.Error.Data.Message
	}
	if msg != "" {
		parts = append(parts, msg)
	}
	if ev.Error.Data.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("status %d", ev.Error.Data.StatusCode))
	}
	if len(parts) == 0 {
		return "unknown error"
	}
	return strings.Join(parts, ": ")
}
