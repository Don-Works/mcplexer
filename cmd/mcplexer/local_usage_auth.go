package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/collectors"
)

const (
	localAuthMaxFileBytes = 1 << 20
	localAuthMaxKeyBytes  = 8192
)

type localAuthTarget struct {
	relPath  string
	provider string
}

var localAuthTargets = map[string]localAuthTarget{
	store.LocalAuthScopeOpenCode + "\x00" + store.LocalAuthKeyMiniMax: {
		relPath:  filepath.Join(".local", "share", "opencode", "auth.json"),
		provider: store.LocalAuthKeyMiniMax,
	},
	store.LocalAuthScopeOpenCode + "\x00" + store.LocalAuthKeyZAI: {
		relPath:  filepath.Join(".local", "share", "opencode", "auth.json"),
		provider: store.LocalAuthKeyZAI,
	},
	store.LocalAuthScopeOpenCode + "\x00" + store.LocalAuthKeyOpenRouter: {
		relPath:  filepath.Join(".local", "share", "opencode", "auth.json"),
		provider: store.LocalAuthKeyOpenRouter,
	},
	store.LocalAuthScopeMiMo + "\x00" + store.LocalAuthKeyMiMoXiaomi: {
		relPath:  filepath.Join(".local", "share", "mimocode", "auth.json"),
		provider: store.LocalAuthKeyMiMoXiaomi,
	},
}

// localUsageAuthReader resolves encrypted auth-scope secrets first, then
// built-in CLI auth files for known local sentinel references.
type localUsageAuthReader struct {
	secrets collectors.SecretReader
	homeDir func() (string, error)
}

func newLocalUsageAuthReader(secrets collectors.SecretReader) collectors.SecretReader {
	return &localUsageAuthReader{secrets: secrets, homeDir: os.UserHomeDir}
}

func (r *localUsageAuthReader) Get(
	ctx context.Context,
	scopeID, key string,
) ([]byte, error) {
	if store.IsLocalAuthRef(scopeID, key) {
		return readLocalCLIAuth(r.homeDir, scopeID, key)
	}
	if r.secrets == nil {
		return nil, fmt.Errorf("secret reader unavailable")
	}
	return r.secrets.Get(ctx, scopeID, key)
}

func readLocalCLIAuth(
	homeDir func() (string, error),
	scopeID, key string,
) ([]byte, error) {
	target, ok := localAuthTargets[scopeID+"\x00"+key]
	if !ok {
		return nil, fmt.Errorf("unsupported local auth reference")
	}
	home, err := homeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory")
	}
	path := filepath.Join(home, target.relPath)
	body, err := readRegularAuthFile(path)
	if err != nil {
		return nil, err
	}
	token, err := extractProviderKey(body, target.provider)
	if err != nil {
		return nil, err
	}
	if len(token) == 0 {
		return nil, fmt.Errorf("credential not present")
	}
	if len(token) > localAuthMaxKeyBytes {
		return nil, fmt.Errorf("credential exceeds size limit")
	}
	return []byte(token), nil
}

func readRegularAuthFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("CLI auth file not found")
		}
		return nil, fmt.Errorf("stat CLI auth file")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("CLI auth file is a symlink")
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("CLI auth file is not a regular file")
	}
	if info.Size() > localAuthMaxFileBytes {
		return nil, fmt.Errorf("CLI auth file exceeds size limit")
	}
	file, err := os.Open(path) //nolint:gosec // path is fixed relative to HOME
	if err != nil {
		return nil, fmt.Errorf("open CLI auth file")
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, localAuthMaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read CLI auth file")
	}
	if len(body) > localAuthMaxFileBytes {
		return nil, fmt.Errorf("CLI auth file exceeds size limit")
	}
	return body, nil
}

func extractProviderKey(body []byte, provider string) (string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return "", fmt.Errorf("decode CLI auth file")
	}
	raw, ok := root[provider]
	if !ok {
		return "", fmt.Errorf("provider entry not found")
	}
	var entry struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return "", fmt.Errorf("decode provider entry")
	}
	return strings.TrimSpace(entry.Key), nil
}
