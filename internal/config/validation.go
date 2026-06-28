package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// ValidationError holds all validation failures for a config file.
type ValidationError struct {
	Errors []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("config validation failed: %s", strings.Join(e.Errors, "; "))
}

// validate checks the parsed config for correctness.
func validate(cfg *FileConfig) error {
	var errs []string

	dsIDs := make(map[string]bool, len(cfg.DownstreamServers))
	nsSet := make(map[string]bool, len(cfg.DownstreamServers))
	for i, ds := range cfg.DownstreamServers {
		if ds.ID == "" {
			errs = append(errs, fmt.Sprintf("downstream_servers[%d]: id is required", i))
		}
		if dsIDs[ds.ID] {
			errs = append(errs, fmt.Sprintf("downstream_servers[%d]: duplicate id %q", i, ds.ID))
		}
		dsIDs[ds.ID] = true
		if ds.ToolNamespace == "" {
			errs = append(errs, fmt.Sprintf("downstream_servers[%d]: tool_namespace is required", i))
		}
		if nsSet[ds.ToolNamespace] {
			errs = append(errs, fmt.Sprintf("downstream_servers[%d]: duplicate namespace %q", i, ds.ToolNamespace))
		}
		nsSet[ds.ToolNamespace] = true
		if err := validateTransport(ds.Transport); err != nil {
			errs = append(errs, fmt.Sprintf("downstream_servers[%d]: %v", i, err))
		}
		if ds.IdleTimeoutSec < 0 || ds.IdleTimeoutSec > 86400 {
			errs = append(errs, fmt.Sprintf("downstream_servers[%d]: idle_timeout_sec must be >= 0 and <= 86400", i))
		}
		if ds.Transport == "http" && ds.URL != "" {
			if err := validateHTTPURL(ds.URL); err != nil {
				errs = append(errs, fmt.Sprintf("downstream_servers[%d]: %v", i, err))
			}
		}
	}

	if cfg.LogRotation != nil {
		if cfg.LogRotation.MaxSizeMB < 0 {
			errs = append(errs, "log_rotation.max_size_mb must be >= 0")
		}
		if cfg.LogRotation.MaxBackups < 0 {
			errs = append(errs, "log_rotation.max_backups must be >= 0")
		}
		if cfg.LogRotation.MaxAgeDays < 0 {
			errs = append(errs, "log_rotation.max_age_days must be >= 0")
		}
	}
	if cfg.AuditRetention != nil {
		if cfg.AuditRetention.AuditDays < 0 {
			errs = append(errs, "audit_retention.audit_days must be >= 0")
		}
		if cfg.AuditRetention.WorkerRunKeepPerWorker < 0 {
			errs = append(errs, "audit_retention.worker_run_keep_per_worker must be >= 0")
		}
		if cfg.AuditRetention.WorkerRunCapDays < 0 {
			errs = append(errs, "audit_retention.worker_run_cap_days must be >= 0")
		}
	}

	if len(errs) > 0 {
		return &ValidationError{Errors: errs}
	}
	return nil
}

func validatePolicy(p string) error {
	switch p {
	case "allow", "deny", "":
		return nil
	default:
		return fmt.Errorf("invalid policy %q (must be allow or deny)", p)
	}
}

func validateTransport(t string) error {
	switch t {
	case "stdio", "http", "internal", "":
		return nil
	default:
		return fmt.Errorf("invalid transport %q (must be stdio, http, or internal)", t)
	}
}

func validateGlob(pattern string) error {
	if pattern == "" {
		return nil
	}
	_, err := filepath.Match(pattern, "test")
	if err != nil {
		return fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
	}
	return nil
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url %q must use http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q must include a host", raw)
	}
	return nil
}

// ValidateCallTimeoutSec checks that a call_timeout_sec value is within bounds.
// Returns nil for 0 (means "use default"). Non-zero must be > 0 and < 3600.
func ValidateCallTimeoutSec(v int) error {
	if v == 0 {
		return nil
	}
	if v < 0 || v > 3600 {
		return fmt.Errorf("call_timeout_sec must be > 0 and <= 3600, got %d", v)
	}
	return nil
}
