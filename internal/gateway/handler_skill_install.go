package gateway

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

// handleSkillInstall implements mcpx__skill_install. Fetches the bundle
// for (name, version) from the registry and extracts the tar.gz onto
// disk at dest. Refuses to overwrite an existing directory unless
// overwrite=true. Returns the resolved dest plus the list of extracted
// files in a single tool-result block.
func (h *handler) handleSkillInstall(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	var args struct {
		Name      string          `json:"name"`
		Version   json.RawMessage `json:"version"`
		Dest      string          `json:"dest"`
		Overwrite bool            `json:"overwrite"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("name", args.Name,
		"skill name as registered — call mcpx__skill_list to browse")

	var refVal any
	if len(args.Version) > 0 && string(args.Version) != "null" {
		if err := json.Unmarshal(args.Version, &refVal); err != nil {
			v.addFieldErr("invalid_value", "version", string(args.Version),
				fmt.Sprintf("invalid version: %v", err),
				"pass an integer version number or omit for latest")
		}
	}
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	ref, err := skillregistry.ParseVersionRef(refVal)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}

	bundle, sha, err := h.skillRegistry.FetchBundle(ctx, h.sessionScope(ctx), args.Name, ref)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult(fmt.Sprintf("Skill %q not found.", args.Name)), nil
	}
	if errors.Is(err, skillregistry.ErrBundleNotPresent) {
		return marshalErrorResult(fmt.Sprintf(
			"Skill %q has no bundle attached — it's a text-only registry entry. Use mcpx__skill_get to read its SKILL.md instead.",
			args.Name,
		)), nil
	}
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}

	dest, err := resolveSkillInstallDest(args.Dest, args.Name)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	files, err := extractSkillBundle(bundle, dest, args.Overwrite)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("install failed: %v", err)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Installed %s @ %s\n  dest: %s\n  bundle_sha256: %s\n  files (%d):\n",
		args.Name, ref.String(), dest, sha, len(files))
	for _, f := range files {
		fmt.Fprintf(&b, "    %s\n", f)
	}
	return marshalToolResult(b.String()), nil
}

// resolveSkillInstallDest expands the user-supplied dest. Empty defaults
// to $HOME/.claude/skills/<name>/. Relative paths are rejected — we
// won't guess the right CWD for an installer the agent triggered.
func resolveSkillInstallDest(dest, name string) (string, error) {
	if dest == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve $HOME: %w", err)
		}
		return filepath.Join(home, ".claude", "skills", name), nil
	}
	if strings.HasPrefix(dest, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve ~: %w", err)
		}
		dest = filepath.Join(home, strings.TrimPrefix(dest, "~/"))
	}
	if !filepath.IsAbs(dest) {
		return "", fmt.Errorf("dest must be absolute or start with ~/ (got %q)", dest)
	}
	return filepath.Clean(dest), nil
}

// extractSkillBundle unpacks the tar.gz at dest. Strips a single
// leading directory component when every entry shares one — both
// `tar -czf foo.tgz skill-name/` and `tar -czf foo.tgz ./` produce
// valid bundles. Refuses to overwrite an existing dest unless
// overwrite=true. Returns the list of files written (relative to dest).
func extractSkillBundle(raw []byte, dest string, overwrite bool) ([]string, error) {
	if _, err := os.Stat(dest); err == nil {
		if !overwrite {
			return nil, fmt.Errorf("%s already exists (pass overwrite=true to replace)", dest)
		}
		if err := os.RemoveAll(dest); err != nil {
			return nil, fmt.Errorf("remove existing dest: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat dest: %w", err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir dest: %w", err)
	}

	prefix, err := commonLeadingDir(raw)
	if err != nil {
		return nil, err
	}
	return writeBundleEntries(raw, dest, prefix)
}

// commonLeadingDir returns the single top-level directory shared by
// every entry in the tar.gz, or "" when there's none (entries live at
// the archive root).
func commonLeadingDir(raw []byte) (string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close() //nolint:errcheck
	tr := tar.NewReader(gz)
	var prefix string
	first := true
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("tar next: %w", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(hdr.Name)), "./")
		if name == "" || name == "." {
			continue
		}
		top := name
		if i := strings.Index(name, "/"); i > 0 {
			top = name[:i]
		} else if hdr.Typeflag != tar.TypeDir {
			return "", nil
		}
		if first {
			prefix = top
			first = false
			continue
		}
		if top != prefix {
			return "", nil
		}
	}
	return prefix, nil
}

// writeBundleEntries materialises every regular file in the tar.gz at
// dest, stripping prefix (when non-empty). Symlinks and devices are
// silently skipped — skill bundles don't need them and refusing is the
// safer default for agent-triggered installs.
func writeBundleEntries(raw []byte, dest, prefix string) ([]string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close() //nolint:errcheck
	tr := tar.NewReader(gz)

	var written []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar next: %w", err)
		}
		rel := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(hdr.Name)), "./")
		if prefix != "" {
			rel = strings.TrimPrefix(rel, prefix+"/")
			if rel == prefix {
				continue
			}
		}
		if rel == "" || rel == "." || strings.HasPrefix(rel, "..") || strings.HasPrefix(rel, "/") {
			continue
		}
		out := filepath.Join(dest, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return nil, fmt.Errorf("mkdir %s: %w", rel, err)
			}
		case tar.TypeReg, 0:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return nil, fmt.Errorf("mkdir parent of %s: %w", rel, err)
			}
			mode := os.FileMode(hdr.Mode & 0o777)
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return nil, fmt.Errorf("create %s: %w", rel, err)
			}
			if _, err := io.Copy(f, io.LimitReader(tr, 50*1024*1024)); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("write %s: %w", rel, err)
			}
			if err := f.Close(); err != nil {
				return nil, fmt.Errorf("close %s: %w", rel, err)
			}
			written = append(written, rel)
		}
	}
	return written, nil
}
