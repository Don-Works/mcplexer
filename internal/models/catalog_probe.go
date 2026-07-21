package models

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoLiveModelSource signals that a provider has no deterministic,
// non-auth way to enumerate its models right now, so the catalog must fall
// back to the declared KnownModels and label the entry static. It is a
// sentinel (not a failure): the refresher treats it exactly like a static
// provider, without an alarming "probe failed" note.
var ErrNoLiveModelSource = errors.New("models: no live model source for provider")

// ProbeResult is what a live probe yields: the enumerated model ids, the
// auth state observed while enumerating, and an optional operator note.
type ProbeResult struct {
	Models    []string
	AuthState ModelAuthState
	Note      string
}

// ModelProber enumerates one provider's currently-available models from the
// provider itself. Probe must be deterministic and cheap — a CLI listing
// command or a local config file, never a model/inference call — and must
// respect ctx (it runs on the refresh cadence, off the delegation path).
type ModelProber interface {
	Provider() string
	Probe(ctx context.Context) (ProbeResult, error)
}

// probeCommandRunner is the exec seam. Tests inject a fake returning
// captured fixture bytes; production uses execProbeRunner.
type probeCommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// catalogProbeTimeout bounds a single listing command so one wedged CLI
// cannot stall the refresh loop. Listing is local/quick (no inference), so
// this is generous headroom, not a normal-case latency.
const catalogProbeTimeout = 12 * time.Second

// execProbeRunner runs a listing command, returning its stdout. stderr is
// folded into the error only, so a chatty CLI banner never pollutes the
// parsed model list.
func execProbeRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf(
			"%s %s: %w (stderr: %s)",
			name, strings.Join(args, " "), err, truncate(stderr.String(), 200))
	}
	return stdout.Bytes(), nil
}

// grokModelsProber shells `grok models` — grok's own "list available models
// and exit" subcommand. It also surfaces grok's logged-in/out state.
type grokModelsProber struct {
	binary string
	run    probeCommandRunner
}

func newGrokModelsProber() *grokModelsProber {
	return &grokModelsProber{
		binary: resolveBinaryPath(grokCLIDefaultBinary, GrokCLIBinaryEnvVar, grokStandardPaths),
		run:    execProbeRunner,
	}
}

func (p *grokModelsProber) Provider() string { return ProviderGrokCLI }

func (p *grokModelsProber) Probe(ctx context.Context) (ProbeResult, error) {
	out, err := p.run(ctx, p.binary, "models")
	ids, auth := parseGrokModelsList(out)
	if len(ids) == 0 {
		if err != nil {
			return ProbeResult{}, err
		}
		return ProbeResult{}, ErrNoLiveModelSource
	}
	note := ""
	if auth == ModelAuthUnauthenticated {
		note = "unauthenticated — showing the CLI default model only"
	}
	return ProbeResult{Models: ids, AuthState: auth, Note: note}, nil
}

// mimoModelsProber shells `mimo models` — one provider/model id per line.
type mimoModelsProber struct {
	binary string
	run    probeCommandRunner
}

func newMimoModelsProber() *mimoModelsProber {
	return &mimoModelsProber{
		binary: resolveBinaryPath(mimoCLIDefaultBinary, MiMoCLIBinaryEnvVar, mimoStandardPaths),
		run:    execProbeRunner,
	}
}

func (p *mimoModelsProber) Provider() string { return ProviderMiMoCLI }

func (p *mimoModelsProber) Probe(ctx context.Context) (ProbeResult, error) {
	out, err := p.run(ctx, p.binary, "models")
	ids := parseMimoModelsList(out)
	if len(ids) == 0 {
		if err != nil {
			return ProbeResult{}, err
		}
		return ProbeResult{}, ErrNoLiveModelSource
	}
	return ProbeResult{Models: ids, AuthState: ModelAuthOK}, nil
}

// piModelsFileProber reads ~/.pi/agent/models.json — Pi's own routing config
// and the authoritative source of the model ids/aliases Pi will accept. A
// file read (no subprocess, no auth) is the cleanest live source available.
type piModelsFileProber struct {
	path string
	read func(string) ([]byte, error)
}

func newPiModelsFileProber() *piModelsFileProber {
	path := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		path = filepath.Join(home, ".pi", "agent", "models.json")
	}
	return &piModelsFileProber{path: path, read: os.ReadFile}
}

func (p *piModelsFileProber) Provider() string { return ProviderPiCLI }

func (p *piModelsFileProber) Probe(ctx context.Context) (ProbeResult, error) {
	if p.path == "" {
		return ProbeResult{}, ErrNoLiveModelSource
	}
	raw, err := p.read(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ProbeResult{}, ErrNoLiveModelSource
		}
		return ProbeResult{}, fmt.Errorf("read pi models.json: %w", err)
	}
	ids, err := parsePiModelsFile(raw)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("parse pi models.json: %w", err)
	}
	if len(ids) == 0 {
		return ProbeResult{}, ErrNoLiveModelSource
	}
	return ProbeResult{
		Models:    ids,
		AuthState: ModelAuthNotApplicable,
		Note:      "live from ~/.pi/agent/models.json",
	}, nil
}

// EnabledProbers returns the live probers for every CLI provider whose env
// opt-in is set. Providers without an opt-in are skipped: the daemon cannot
// run them, so probing them would only produce noise.
func EnabledProbers() []ModelProber {
	var out []ModelProber
	if CLIProviderAllowed(ProviderGrokCLI) {
		out = append(out, newGrokModelsProber())
	}
	if CLIProviderAllowed(ProviderMiMoCLI) {
		out = append(out, newMimoModelsProber())
	}
	if CLIProviderAllowed(ProviderPiCLI) {
		out = append(out, newPiModelsFileProber())
	}
	return out
}

// EnabledCLIProviders lists the CLI providers whose env opt-in is set, so the
// catalog always carries a row for every provider the daemon can actually
// dispatch — even one with no live source (it shows as static) or no declared
// models yet (it shows empty). This is what makes "what can I route to?"
// answerable without cross-referencing env vars and model profiles by hand.
func EnabledCLIProviders() []string {
	all := []string{
		ProviderClaudeCLI, ProviderOpenCodeCLI, ProviderGrokCLI,
		ProviderMiMoCLI, ProviderGeminiCLI, ProviderCodexCLI, ProviderPiCLI,
	}
	out := make([]string, 0, len(all))
	for _, p := range all {
		if CLIProviderAllowed(p) {
			out = append(out, p)
		}
	}
	return out
}
