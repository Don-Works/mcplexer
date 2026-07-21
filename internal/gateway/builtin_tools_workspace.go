package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/don-works/mcplexer/internal/pathguard"
)

const (
	workspaceFileMaxBytes   = 1 << 20
	workspaceListMaxEntries = 1000
)

var workspaceMutationLocks [64]sync.Mutex

func workspaceToolDefinitions() []Tool {
	return []Tool{
		{Name: "mcpx__workspace_read_file", Description: "Read one bounded regular file from the authenticated isolated worktree. Paths are workspace-relative; .git is never accessible.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`), Extras: withAnnotations(ToolAnnotations{ReadOnlyHint: boolPtr(true)})},
		{Name: "mcpx__workspace_list_directory", Description: "List one directory in the authenticated isolated worktree (bounded, sorted, non-recursive). Paths are workspace-relative; .git is never accessible.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","default":"."}},"additionalProperties":false}`), Extras: withAnnotations(ToolAnnotations{ReadOnlyHint: boolPtr(true)})},
		{Name: "mcpx__workspace_write_file", Description: "Atomically write one bounded regular file inside the authenticated isolated worktree and declared touches_files scope.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`), Extras: withAnnotations(ToolAnnotations{ReadOnlyHint: boolPtr(false)})},
		{Name: "mcpx__workspace_edit_file", Description: "Atomically replace an exact expected number of text occurrences in one bounded regular file inside the authenticated isolated worktree and declared touches_files scope.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"},"expected_replacements":{"type":"integer","minimum":1,"default":1}},"required":["path","old_text","new_text"],"additionalProperties":false}`), Extras: withAnnotations(ToolAnnotations{ReadOnlyHint: boolPtr(false)})},
	}
}

func (h *handler) handleWorkspaceTool(ctx context.Context, req CallToolRequest) (json.RawMessage, *RPCError) {
	scope, ok := workerFilesystemScopeFromContext(ctx)
	if !ok {
		return nil, &RPCError{Code: CodeInvalidRequest, Message: "workspace file tools require a live authenticated isolated-worker scope"}
	}
	root, err := os.OpenRoot(scope.Root())
	if err != nil {
		return nil, workspaceRPCError(err)
	}
	defer root.Close() //nolint:errcheck

	switch req.Name {
	case "mcpx__workspace_read_file":
		var args struct {
			Path string `json:"path"`
		}
		if err := decodeWorkspaceArgs(req.Arguments, &args); err != nil {
			return nil, workspaceRPCError(err)
		}
		rel, err := workspaceReadPath(scope, args.Path)
		if err != nil {
			return nil, workspaceRPCError(err)
		}
		data, err := readWorkspaceRegular(root, rel)
		if err != nil {
			return nil, workspaceRPCError(err)
		}
		if !utf8.Valid(data) {
			return nil, workspaceRPCError(errors.New("file is not valid UTF-8 text"))
		}
		return workspaceJSONResult(map[string]any{"path": filepath.ToSlash(args.Path), "content": string(data), "bytes": len(data)})

	case "mcpx__workspace_list_directory":
		var args struct {
			Path string `json:"path"`
		}
		if err := decodeWorkspaceArgs(req.Arguments, &args); err != nil {
			return nil, workspaceRPCError(err)
		}
		if args.Path == "" {
			args.Path = "."
		}
		rel, err := workspaceReadPath(scope, args.Path)
		if err != nil {
			return nil, workspaceRPCError(err)
		}
		f, err := root.Open(rel)
		if err != nil {
			return nil, workspaceRPCError(err)
		}
		defer f.Close() //nolint:errcheck
		info, err := f.Stat()
		if err != nil || !info.IsDir() {
			if err == nil {
				err = errors.New("target is not a directory")
			}
			return nil, workspaceRPCError(err)
		}
		entries, err := f.ReadDir(workspaceListMaxEntries + 1)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, workspaceRPCError(err)
		}
		if len(entries) > workspaceListMaxEntries {
			return nil, workspaceRPCError(errors.New("directory contains too many entries"))
		}
		type listed struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		out := make([]listed, 0, len(entries))
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), ".git") {
				continue
			}
			kind := "file"
			if entry.IsDir() {
				kind = "directory"
			} else if entry.Type()&os.ModeSymlink != 0 {
				kind = "symlink"
			} else if !entry.Type().IsRegular() {
				kind = "other"
			}
			out = append(out, listed{Name: entry.Name(), Type: kind})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return workspaceJSONResult(map[string]any{"path": filepath.ToSlash(args.Path), "entries": out})

	case "mcpx__workspace_write_file":
		var args struct {
			Path    string  `json:"path"`
			Content *string `json:"content"`
		}
		if err := decodeWorkspaceArgs(req.Arguments, &args); err != nil {
			return nil, workspaceRPCError(err)
		}
		if args.Content == nil {
			return nil, workspaceRPCError(errors.New("content is required"))
		}
		if len(*args.Content) > workspaceFileMaxBytes {
			return nil, workspaceRPCError(errors.New("content exceeds 1 MiB limit"))
		}
		unlock := lockWorkspaceMutation(scope.Root())
		defer unlock()
		rel, err := workspaceWritePath(scope, args.Path)
		if err != nil {
			return nil, workspaceRPCError(err)
		}
		writeRoot, target, err := openWorkspaceClaimRoot(root, scope, rel)
		if err != nil {
			return nil, workspaceRPCError(err)
		}
		defer writeRoot.Close() //nolint:errcheck
		if err := atomicWorkspaceWrite(writeRoot, target, []byte(*args.Content)); err != nil {
			return nil, workspaceRPCError(err)
		}
		return workspaceJSONResult(map[string]any{"path": filepath.ToSlash(args.Path), "bytes": len(*args.Content)})

	case "mcpx__workspace_edit_file":
		var args struct {
			Path     string  `json:"path"`
			OldText  string  `json:"old_text"`
			NewText  *string `json:"new_text"`
			Expected *int    `json:"expected_replacements"`
		}
		if err := decodeWorkspaceArgs(req.Arguments, &args); err != nil {
			return nil, workspaceRPCError(err)
		}
		if args.OldText == "" {
			return nil, workspaceRPCError(errors.New("old_text must not be empty"))
		}
		if args.NewText == nil {
			return nil, workspaceRPCError(errors.New("new_text is required"))
		}
		expected := 1
		if args.Expected != nil {
			expected = *args.Expected
		}
		if expected < 1 {
			return nil, workspaceRPCError(errors.New("expected_replacements must be at least 1"))
		}
		unlock := lockWorkspaceMutation(scope.Root())
		defer unlock()
		rel, err := workspaceWritePath(scope, args.Path)
		if err != nil {
			return nil, workspaceRPCError(err)
		}
		data, err := readWorkspaceRegular(root, rel)
		if err != nil {
			return nil, workspaceRPCError(err)
		}
		if !utf8.Valid(data) {
			return nil, workspaceRPCError(errors.New("file is not valid UTF-8 text"))
		}
		count := strings.Count(string(data), args.OldText)
		if count != expected {
			return nil, workspaceRPCError(fmt.Errorf("replacement count %d does not match expected %d", count, expected))
		}
		updated := strings.ReplaceAll(string(data), args.OldText, *args.NewText)
		if len(updated) > workspaceFileMaxBytes {
			return nil, workspaceRPCError(errors.New("edited content exceeds 1 MiB limit"))
		}
		writeRoot, target, err := openWorkspaceClaimRoot(root, scope, rel)
		if err != nil {
			return nil, workspaceRPCError(err)
		}
		defer writeRoot.Close() //nolint:errcheck
		if err := atomicWorkspaceWrite(writeRoot, target, []byte(updated)); err != nil {
			return nil, workspaceRPCError(err)
		}
		return workspaceJSONResult(map[string]any{"path": filepath.ToSlash(args.Path), "replacements": count, "bytes": len(updated)})
	default:
		return nil, &RPCError{Code: CodeMethodNotFound, Message: "unknown workspace tool"}
	}
}

func decodeWorkspaceArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("arguments must contain one JSON object")
	}
	return nil
}

func workspaceReadPath(scope pathguard.Scope, input string) (string, error) {
	if err := rejectWorkspacePath(input); err != nil {
		return "", err
	}
	rel, err := scope.Relative(input)
	if err != nil {
		return "", err
	}
	if err := rejectResolvedGitPath(rel); err != nil {
		return "", err
	}
	return rel, nil
}

func workspaceWritePath(scope pathguard.Scope, input string) (string, error) {
	if err := rejectWorkspacePath(input); err != nil {
		return "", err
	}
	lexical, err := scope.LexicalRelative(input)
	if err != nil {
		return "", err
	}
	resolved, err := scope.AllowsWriteRelative(input)
	if err != nil {
		return "", err
	}
	if err := rejectResolvedGitPath(resolved); err != nil {
		return "", err
	}
	if filepath.Clean(lexical) != filepath.Clean(resolved) {
		return "", errors.New("refusing to write or edit through a symlink alias")
	}
	return resolved, nil
}

func rejectWorkspacePath(input string) error {
	if input == "" {
		return errors.New("path is required")
	}
	if strings.Contains(input, `\`) {
		return errors.New("path must use workspace-relative '/' separators")
	}
	clean := strings.ReplaceAll(input, `\`, "/")
	if strings.HasPrefix(clean, "/") {
		return errors.New("path must be workspace-relative")
	}
	if strings.Contains(clean, ":") {
		return errors.New("path must not contain ':'")
	}
	for _, segment := range strings.Split(clean, "/") {
		if segment == ".." {
			return errors.New("path must not contain '..' traversal segments")
		}
	}
	return rejectResolvedGitPath(clean)
}

func rejectResolvedGitPath(path string) error {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.EqualFold(strings.TrimRight(segment, ". "), ".git") {
			return errors.New(".git is never accessible to isolated workers")
		}
	}
	return nil
}

func readWorkspaceRegular(root *os.Root, rel string) ([]byte, error) {
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("target is not a regular file")
	}
	if info.Size() > workspaceFileMaxBytes {
		return nil, errors.New("file exceeds 1 MiB limit")
	}
	data, err := io.ReadAll(io.LimitReader(f, workspaceFileMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > workspaceFileMaxBytes {
		return nil, errors.New("file exceeds 1 MiB limit")
	}
	return data, nil
}

func atomicWorkspaceWrite(root *os.Root, rel string, data []byte) error {
	if len(data) > workspaceFileMaxBytes {
		return errors.New("content exceeds 1 MiB limit")
	}
	mode := fs.FileMode(0o644)
	if info, err := root.Lstat(rel); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("refusing to write through a symlink")
		}
		if !info.Mode().IsRegular() {
			return errors.New("target is not a regular file")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(rel)
	if parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := filepath.Join(parent, ".mcplexer-tmp-"+hex.EncodeToString(nonce[:]))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = root.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Chmod(mode)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := root.Rename(tmp, rel); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func lockWorkspaceMutation(root string) func() {
	// Fixed stripes avoid an unbounded root-keyed lock map while ensuring all
	// write/edit calls for one isolated worktree serialize. That makes
	// expected_replacements a real compare-and-swap boundary for edits.
	const (
		fnvOffset = uint32(2166136261)
		fnvPrime  = uint32(16777619)
	)
	h := fnvOffset
	for i := 0; i < len(root); i++ {
		h ^= uint32(root[i])
		h *= fnvPrime
	}
	mu := &workspaceMutationLocks[int(h)%len(workspaceMutationLocks)]
	mu.Lock()
	return mu.Unlock
}

func openWorkspaceClaimRoot(root *os.Root, scope pathguard.Scope, targetRel string) (*os.Root, string, error) {
	targetAbs := filepath.Join(scope.Root(), targetRel)
	base := scope.Root()
	claims := scope.Claims()
	if len(claims) > 0 {
		matched := ""
		for _, claim := range claims {
			rel, err := filepath.Rel(claim, targetAbs)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				if len(claim) > len(matched) {
					matched = claim
				}
			}
		}
		if matched == "" {
			return nil, "", errors.New("target is outside declared touches_files")
		}
		base = matched
		if info, err := os.Stat(base); err == nil && !info.IsDir() {
			base = filepath.Dir(base)
		}
		for {
			info, err := os.Stat(base)
			if err == nil && info.IsDir() {
				break
			}
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, "", err
			}
			parent := filepath.Dir(base)
			if parent == base || !workspacePathWithin(scope.Root(), parent) {
				return nil, "", errors.New("no safe existing claim ancestor")
			}
			base = parent
		}
	}
	baseRel, err := filepath.Rel(scope.Root(), base)
	if err != nil {
		return nil, "", err
	}
	claimRoot, err := root.OpenRoot(baseRel)
	if err != nil {
		return nil, "", err
	}
	target, err := filepath.Rel(base, targetAbs)
	if err != nil || target == ".." || strings.HasPrefix(target, ".."+string(filepath.Separator)) || filepath.IsAbs(target) {
		claimRoot.Close() //nolint:errcheck
		return nil, "", errors.New("target escapes claim root")
	}
	return claimRoot, target, nil
}

func workspacePathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)))
}

func workspaceJSONResult(value any) (json.RawMessage, *RPCError) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, workspaceRPCError(err)
	}
	return marshalToolResult(string(data)), nil
}

func workspaceRPCError(err error) *RPCError {
	return &RPCError{Code: CodeInvalidParams, Message: err.Error()}
}
