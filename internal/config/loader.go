package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"gopkg.in/yaml.v3"
)

// FileConfig represents the top-level mcplexer.yaml structure.
type FileConfig struct {
	DownstreamServers []downstreamServerConfig `yaml:"downstream_servers"`
	// LogRotation is optional. When unset, the daemon falls back to the
	// in-code defaults (50MB / 5 backups / 30d / gzip).
	LogRotation *LogRotationFileConfig `yaml:"log_rotation,omitempty"`
	// AuditRetention configures the nightly prune of audit_records and
	// worker_runs. Omitting the block uses DefaultAuditRetention().
	AuditRetention *AuditRetentionConfig `yaml:"audit_retention,omitempty"`
}

// LogRotationFileConfig mirrors cmd/mcplexer.LogRotationConfig in the YAML
// schema. Zero-valued fields fall back to the runtime defaults.
type LogRotationFileConfig struct {
	MaxSizeMB  int   `yaml:"max_size_mb,omitempty"`
	MaxBackups int   `yaml:"max_backups,omitempty"`
	MaxAgeDays int   `yaml:"max_age_days,omitempty"`
	Compress   *bool `yaml:"compress,omitempty"`
}

// AuditRetentionConfig mirrors scheduler.PrunePolicy in the YAML
// surface so operators can extend retention without rebuilding the
// daemon. All fields default to DefaultAuditRetention() when zero.
type AuditRetentionConfig struct {
	// AuditDays is the age cap for audit_records. 0 disables.
	AuditDays int `yaml:"audit_days"`
	// WorkerRunKeepPerWorker is the per-worker floor for worker_runs.
	WorkerRunKeepPerWorker int `yaml:"worker_run_keep_per_worker"`
	// WorkerRunCapDays is the age cap for worker_runs (subject to the
	// per-worker floor). 0 disables the age cap; floor still applies.
	WorkerRunCapDays int `yaml:"worker_run_cap_days"`
}

// DefaultAuditRetention returns the out-of-the-box retention shape
// (90d audit, 1000-per-worker runs with a 180d cap).
func DefaultAuditRetention() AuditRetentionConfig {
	return AuditRetentionConfig{
		AuditDays:              90,
		WorkerRunKeepPerWorker: 1000,
		WorkerRunCapDays:       180,
	}
}

// ResolveRetention merges YAML overrides with defaults.
func (c *FileConfig) ResolveRetention() AuditRetentionConfig {
	def := DefaultAuditRetention()
	if c == nil || c.AuditRetention == nil {
		return def
	}
	out := *c.AuditRetention
	if out.AuditDays == 0 {
		out.AuditDays = def.AuditDays
	}
	if out.WorkerRunKeepPerWorker == 0 {
		out.WorkerRunKeepPerWorker = def.WorkerRunKeepPerWorker
	}
	if out.WorkerRunCapDays == 0 {
		out.WorkerRunCapDays = def.WorkerRunCapDays
	}
	return out
}

type downstreamServerConfig struct {
	ID             string         `yaml:"id"`
	Name           string         `yaml:"name"`
	Transport      string         `yaml:"transport"`
	Command        string         `yaml:"command"`
	Args           []string       `yaml:"args,omitempty"`
	URL            string         `yaml:"url,omitempty"`
	ToolNamespace  string         `yaml:"tool_namespace"`
	Discovery      string         `yaml:"discovery,omitempty"` // "dynamic" (default) or "static"
	IdleTimeoutSec int            `yaml:"idle_timeout_sec"`
	MaxInstances   int            `yaml:"max_instances"`
	RestartPolicy  string         `yaml:"restart_policy"`
	Cache          map[string]any `yaml:"cache,omitempty"` // optional per-server cache config
}

// LoadFile reads, parses, and validates a YAML config file.
func LoadFile(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	return Parse(data)
}

// Parse parses and validates YAML config data.
func Parse(data []byte) (*FileConfig, error) {
	var cfg FileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Apply upserts downstream servers from config into the store.
// Items from YAML are tagged with source="yaml". Stale yaml-sourced rows
// that no longer appear in the file are deleted automatically.
func Apply(ctx context.Context, s store.Store, cfg *FileConfig) error {
	return s.Tx(ctx, func(tx store.Store) error {
		return applyDownstreamServers(ctx, tx, cfg.DownstreamServers)
	})
}

func applyDownstreamServers(ctx context.Context, tx store.Store, items []downstreamServerConfig) error {
	yamlIDs := make(map[string]bool, len(items))
	for _, d := range items {
		yamlIDs[d.ID] = true
		args, err := json.Marshal(d.Args)
		if err != nil {
			return fmt.Errorf("marshal args for %s: %w", d.ID, err)
		}
		var cacheCfg json.RawMessage
		if len(d.Cache) > 0 {
			cacheCfg, err = json.Marshal(d.Cache)
			if err != nil {
				return fmt.Errorf("marshal cache config for %s: %w", d.ID, err)
			}
		}
		ds := &store.DownstreamServer{
			ID: d.ID, Name: d.Name, Transport: d.Transport,
			Command: d.Command, Args: args, ToolNamespace: d.ToolNamespace,
			Discovery: d.Discovery, IdleTimeoutSec: d.IdleTimeoutSec,
			MaxInstances: d.MaxInstances, RestartPolicy: d.RestartPolicy,
			CacheConfig: cacheCfg,
			Source:      "yaml", UpdatedAt: time.Now().UTC(),
		}
		if d.URL != "" {
			ds.URL = &d.URL
		}
		existing, err := tx.GetDownstreamServer(ctx, d.ID)
		if err != nil {
			ds.CreatedAt = time.Now().UTC()
			if err := tx.CreateDownstreamServer(ctx, ds); err != nil {
				return fmt.Errorf("create downstream %s: %w", d.ID, err)
			}
			continue
		}
		ds.CreatedAt = existing.CreatedAt
		ds.CapabilitiesCache = existing.CapabilitiesCache
		if err := tx.UpdateDownstreamServer(ctx, ds); err != nil {
			return fmt.Errorf("update downstream %s: %w", d.ID, err)
		}
	}
	return pruneStaleDownstreams(ctx, tx, yamlIDs)
}

func pruneStaleDownstreams(ctx context.Context, tx store.Store, yamlIDs map[string]bool) error {
	all, err := tx.ListDownstreamServers(ctx)
	if err != nil {
		return fmt.Errorf("list downstreams for prune: %w", err)
	}
	for _, d := range all {
		if d.Source == "yaml" && !yamlIDs[d.ID] {
			slog.Info("pruning stale yaml downstream", "id", d.ID)
			if err := tx.DeleteDownstreamServer(ctx, d.ID); err != nil {
				return fmt.Errorf("delete stale downstream %s: %w", d.ID, err)
			}
		}
	}
	return nil
}
