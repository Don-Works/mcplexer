package brain

import (
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

func normalizePersonWorkspace(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" || workspace == "global" {
		return store.PersonDefaultWorkspaceID
	}
	return workspace
}

// safePersonWorkspace normalizes and then validates a workspace identifier
// for use in path construction. If the identifier contains path-traversal
// characters (/, \, ..) or other unsafe content rejected by safeSlug, it
// falls back to PersonDefaultWorkspaceID so the file always lands under the
// intended brain workspace directory.
func safePersonWorkspace(workspace string) string {
	candidate := normalizePersonWorkspace(workspace)
	if _, err := safeSlug(candidate); err != nil {
		return store.PersonDefaultWorkspaceID
	}
	return candidate
}

func personWorkspaceForPath(path, explicit string) string {
	if strings.TrimSpace(explicit) != "" && strings.TrimSpace(explicit) != "global" {
		return strings.TrimSpace(explicit)
	}
	if ws := workspaceFromPath(path); ws != "" {
		return ws
	}
	return store.PersonDefaultWorkspaceID
}
