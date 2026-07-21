package models

import (
	"bufio"
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// This file holds the pure, deterministic parsers for each provider's own
// model-listing output. They take raw bytes (a CLI's stdout or a config
// file) and return model ids — no process spawning, no I/O, no model calls —
// so they are exhaustively table-testable against captured real output.

// modelIDLine matches a bare model identifier token: letters/digits then the
// punctuation model ids use (`.`, `_`, `:`, `@`, `/`, `-`). Anchored so a
// line with surrounding prose (spaces, tabs, columns) is rejected — the mimo
// listing is one clean id per line.
var modelIDLine = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]*$`)

// parseGrokModelsList parses `grok models` output, e.g.:
//
//	You are logged in with grok.com.
//
//	Default model: grok-4.5
//
//	Available models:
//	  * grok-4.5 (default)
//
// It returns the listed ids and the auth state inferred from the header
// (grok prints whether the session is logged in). Bullet annotations like
// "(default)" are stripped.
func parseGrokModelsList(raw []byte) ([]string, ModelAuthState) {
	auth := ModelAuthUnknown
	var ids []string
	inList := false
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 8*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if a, ok := grokAuthFromLine(line); ok {
			auth = a
		}
		if strings.EqualFold(strings.TrimRight(line, ":"), "available models") {
			inList = true
			continue
		}
		if id, ok := grokBulletID(line); ok {
			inList = true // tolerate a missing header
			ids = append(ids, id)
			continue
		}
		if inList && strings.Contains(line, ":") {
			inList = false // a new labelled section ends the model list
		}
	}
	return ids, auth
}

// grokAuthFromLine maps grok's status header to an auth state.
func grokAuthFromLine(line string) (ModelAuthState, bool) {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "not logged in"), strings.Contains(l, "please log in"),
		strings.Contains(l, "please login"), strings.Contains(l, "unauthenticated"):
		return ModelAuthUnauthenticated, true
	case strings.Contains(l, "logged in"):
		return ModelAuthOK, true
	default:
		return ModelAuthUnknown, false
	}
}

// grokBulletID extracts the id from a bullet line ("* grok-4.5 (default)").
func grokBulletID(line string) (string, bool) {
	for _, bullet := range []string{"* ", "- ", "• "} {
		if strings.HasPrefix(line, bullet) {
			id := strings.TrimSpace(strings.TrimPrefix(line, bullet))
			if i := strings.Index(id, " ("); i >= 0 {
				id = id[:i] // drop "(default)" and similar annotations
			}
			id = strings.TrimSpace(id)
			if modelIDLine.MatchString(id) {
				return id, true
			}
		}
	}
	return "", false
}

// parseMimoModelsList parses `mimo models` output — one `provider/model` id
// per line, no header. mimo ids are always in `provider/model` form (per the
// CLI's own `-m provider/model` contract), so a bare model token is required
// to contain a slash; that drops any trailing prose ("Done.") without a
// mimo-specific denylist.
func parseMimoModelsList(raw []byte) []string {
	var ids []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 8*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, "/") || !modelIDLine.MatchString(line) {
			continue
		}
		ids = append(ids, line)
	}
	return ids
}

// piModelsFile mirrors the shape of ~/.pi/agent/models.json: provider keys
// each holding a models array of {id,name}. Pi resolves --model against
// EITHER field (the `name` is a local alias, e.g. "qwen-local" → id
// "qwen3.6-35b-a3b"), so both are valid identifiers and both are collected.
type piModelsFile struct {
	Providers map[string]struct {
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	} `json:"providers"`
}

// parsePiModelsFile parses ~/.pi/agent/models.json, collecting every model's
// id AND name across all providers. Collecting both avoids false-rejecting a
// proven-working alias like "qwen-local" that never appears as an `id`.
func parsePiModelsFile(raw []byte) ([]string, error) {
	var f piModelsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	var ids []string
	for _, prov := range f.Providers {
		for _, m := range prov.Models {
			if id := strings.TrimSpace(m.ID); id != "" {
				ids = append(ids, id)
			}
			if name := strings.TrimSpace(m.Name); name != "" {
				ids = append(ids, name)
			}
		}
	}
	return ids, nil
}
