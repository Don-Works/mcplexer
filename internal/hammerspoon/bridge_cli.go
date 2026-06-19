package hammerspoon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// cliDriver shells out to the Hammerspoon `hs` CLI to run Lua. It exists for
// zero-config setups where the user hasn't installed the in-app HTTP bridge —
// `hs` ships with Hammerspoon and lives at /usr/local/bin/hs by default.
//
// **Important:** every call spawns a Lua process, which is ~50× slower than
// the HTTP path. Agents in tight loops should select HAMMERSPOON_DRIVER=http
// once they've completed setup.
//
// The wrapper snippet runs the user's code inside pcall and prints the
// {ok, result, err} JSON envelope on stdout — same shape as the HTTP path so
// MCPServer.Call can treat the two transports identically.
type cliDriver struct {
	bin string // resolved at construction; "hs" if empty
}

// NewCLIDriver constructs a CLI-transport bridge. Pass "" to use whatever `hs`
// is on PATH (the production default); tests override with an absolute path
// to a stub script.
func NewCLIDriver(bin string) Bridge {
	if bin == "" {
		bin = "hs"
	}
	return &cliDriver{bin: bin}
}

// Exec runs the given Lua snippet via `hs -c`.
//
// Stdout is treated as the JSON envelope. A non-zero exit code with a
// non-empty stderr is folded into envelope.Err — Hammerspoon's `hs` exits 0
// for most user-Lua errors and only non-zero when the CLI itself is
// misconfigured (Hammerspoon.app not running, accessibility denied, etc.),
// so a non-zero exit is almost always an actionable user problem.
func (d *cliDriver) Exec(ctx context.Context, lua string, timeout time.Duration) (Envelope, error) {
	if timeout <= 0 {
		timeout = defaultBridgeTimeout
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	wrapped := wrapLuaForCLI(lua)

	cmd := exec.CommandContext(execCtx, d.bin, "-c", wrapped)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return Envelope{Ok: false, Err: "Hammerspoon CLI timed out"}, nil
		}
		// Path lookup failure surfaces as a clean user message.
		if _, lookupErr := exec.LookPath(d.bin); lookupErr != nil {
			return Envelope{Ok: false, Err: fmt.Sprintf("`%s` not found on PATH. Install Hammerspoon and enable the CLI.", d.bin)}, nil
		}
		errOut := strings.TrimSpace(stderr.String())
		if errOut == "" {
			errOut = err.Error()
		}
		return Envelope{Ok: false, Err: "Hammerspoon CLI failed: " + truncate(errOut, 400)}, nil
	}

	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return Envelope{Ok: false, Err: "Hammerspoon CLI returned empty output"}, nil
	}

	// The wrapper always emits a single line of JSON. If the user's code
	// also printed to stdout we keep only the last line — that's where the
	// envelope ends up.
	if idx := strings.LastIndex(out, "\n"); idx >= 0 {
		out = out[idx+1:]
	}

	var env Envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return Envelope{Ok: false, Err: "Hammerspoon CLI returned malformed JSON: " + truncate(out, 200)}, nil
	}
	return env, nil
}

// wrapLuaForCLI wraps the user's Lua snippet so its return value is encoded
// as the {ok, result, err} envelope on stdout. Mirrors the embedded init.lua
// handler so both transports look identical from MCPServer's POV.
func wrapLuaForCLI(userLua string) string {
	// The user snippet is embedded as a long-bracket Lua string ([==[ … ]==])
	// so it's never re-parsed for special chars. Two equals signs are safe
	// for any snippet that doesn't itself contain the literal "]==]".
	return `local _src = [==[` + userLua + `]==]; ` +
		`local _fn, _ferr = load("return (function() " .. _src .. " end)()", "mcpx-cli", "t"); ` +
		`if not _fn then print(hs.json.encode({ok=false, err="load: "..tostring(_ferr)})) return end; ` +
		`local _ok, _res = pcall(_fn); ` +
		`if not _ok then print(hs.json.encode({ok=false, err=tostring(_res)})) return end; ` +
		`print(hs.json.encode({ok=true, result=_res}))`
}
