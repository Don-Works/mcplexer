package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func cmdConfigShow(args []string) error {
	ctx := context.Background()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	db, err := sqlite.New(ctx, cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	settingsSvc := config.NewSettingsService(db)
	settings := settingsSvc.Load(ctx)

	defaults := config.DefaultSettings()

	sections := []struct {
		name   string
		fields []configField
	}{
		{"Daemon", daemonFields(cfg)},
		{"Networking", networkingFields(cfg)},
		{"Mesh", meshFields(settings, defaults)},
		{"Skills", skillsFields(settings, defaults)},
		{"Logging", loggingFields(cfg, settings, defaults)},
		{"Security", securityFields(settings, defaults)},
		{"Advanced", advancedFields(settings, defaults)},
	}

	fmt.Println("MCPlexer Configuration")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━")

	for _, sec := range sections {
		if len(sec.fields) == 0 {
			continue
		}
		fmt.Printf("\n%s\n", sec.name)
		for _, f := range sec.fields {
			val := f.value
			if val == "" {
				val = "(unset)"
			}
			source := f.source
			if source != "" {
				source = " (" + source + ")"
			}
			fmt.Printf("  %-30s %-20s%s\n", f.name+":", val, source)
		}
	}

	return nil
}

type configField struct {
	name   string
	value  string
	source string
}

func daemonFields(cfg *Config) []configField {
	var fields []configField

	modeSource := "default"
	if os.Getenv("MCPLEXER_MODE") != "" {
		modeSource = "env: MCPLEXER_MODE"
	}
	fields = append(fields, configField{"mode", cfg.Mode, modeSource})

	addrSource := "default"
	if os.Getenv("MCPLEXER_HTTP_ADDR") != "" {
		addrSource = "env: MCPLEXER_HTTP_ADDR"
	}
	fields = append(fields, configField{"http_addr", cfg.HTTPAddr, addrSource})

	dbDriverSource := "default"
	if os.Getenv("MCPLEXER_DB_DRIVER") != "" {
		dbDriverSource = "env: MCPLEXER_DB_DRIVER"
	}
	fields = append(fields, configField{"db_driver", cfg.DBDriver, dbDriverSource})

	dbDSNSource := "default"
	if os.Getenv("MCPLEXER_DB_DSN") != "" {
		dbDSNSource = "env: MCPLEXER_DB_DSN"
	}
	fields = append(fields, configField{"db_dsn", cfg.DBDSN, dbDSNSource})

	profileSource := "default"
	if os.Getenv("MCPLEXER_SERVER_PROFILE") != "" {
		profileSource = "env: MCPLEXER_SERVER_PROFILE"
	}
	fields = append(fields, configField{"server_profile", cfg.ServerProfile, profileSource})

	return fields
}

func networkingFields(cfg *Config) []configField {
	var fields []configField

	socketSource := "default"
	if cfg.SocketPath != "" {
		socketSource = "env: MCPLEXER_SOCKET_PATH"
	}
	fields = append(fields, configField{"socket_path", cfg.SocketPath, socketSource})

	extSource := "default"
	if cfg.ExternalURL != "" {
		extSource = "env: MCPLEXER_EXTERNAL_URL"
	}
	fields = append(fields, configField{"external_url", cfg.ExternalURL, extSource})

	p2pSource := "default"
	if os.Getenv("MCPLEXER_P2P_ENABLED") != "" {
		p2pSource = "env: MCPLEXER_P2P_ENABLED"
	}
	p2pVal := "false"
	if cfg.P2PEnabled {
		p2pVal = "true"
	}
	fields = append(fields, configField{"p2p_enabled", p2pVal, p2pSource})

	return fields
}

func meshFields(settings, defaults config.Settings) []configField {
	var fields []configField

	fields = appendBoolField(fields, "mesh_enabled", settings.MeshEnabled, defaults.MeshEnabled, "MCPLEXER_MESH_ENABLED")
	fields = appendIntField(fields, "mesh_receive_max_results", settings.MeshReceiveMaxResults, defaults.MeshReceiveMaxResults, "MCPLEXER_MESH_RECEIVE_MAX_RESULTS")
	fields = appendIntField(fields, "mesh_receive_preview_bytes", settings.MeshReceivePreviewBytes, defaults.MeshReceivePreviewBytes, "MCPLEXER_MESH_RECEIVE_PREVIEW_BYTES")
	fields = appendIntField(fields, "mesh_send_max_content_bytes", settings.MeshSendMaxContentBytes, defaults.MeshSendMaxContentBytes, "MCPLEXER_MESH_SEND_MAX_CONTENT_BYTES")
	fields = appendBoolField(fields, "mesh_auto_replicate_off", settings.MeshAutoReplicateOff, defaults.MeshAutoReplicateOff, "MCPLEXER_MESH_AUTO_REPLICATE_OFF")

	return fields
}

func skillsFields(settings, defaults config.Settings) []configField {
	var fields []configField

	fields = append(fields, configField{
		name:   "remote_skill_server_url",
		value:  settings.RemoteSkillServerURL,
		source: envSource("MCPLEXER_REMOTE_SKILL_SERVER_URL", settings.RemoteSkillServerURL, defaults.RemoteSkillServerURL),
	})

	return fields
}

func loggingFields(cfg *Config, settings, defaults config.Settings) []configField {
	var fields []configField

	fields = append(fields, configField{
		name:   "log_level",
		value:  settings.LogLevel,
		source: envSource("MCPLEXER_LOG_LEVEL", settings.LogLevel, defaults.LogLevel),
	})

	logPathSource := "default"
	if cfg.LogPath != "" {
		logPathSource = "env: MCPLEXER_LOG_PATH"
	}
	fields = append(fields, configField{"log_path", cfg.LogPath, logPathSource})

	return fields
}

func securityFields(settings, defaults config.Settings) []configField {
	var fields []configField

	fields = appendBoolField(fields, "slim_surface", settings.SlimSurface, defaults.SlimSurface, "MCPLEXER_SLIM_SURFACE")
	fields = appendBoolField(fields, "slim_tools", settings.SlimTools, defaults.SlimTools, "MCPLEXER_SLIM_TOOLS")
	fields = appendBoolField(fields, "sandbox_downstreams", settings.SandboxDownstreams, defaults.SandboxDownstreams, "MCPLEXER_SANDBOX_DOWNSTREAMS")
	fields = appendBoolField(fields, "dangerous_mode_enabled", settings.DangerousModeEnabled, defaults.DangerousModeEnabled, "MCPLEXER_DANGEROUS_MODE_ENABLED")
	fields = appendBoolField(fields, "sanitizer_envelope_always", settings.SanitizerEnvelopeAlways, defaults.SanitizerEnvelopeAlways, "MCPLEXER_SANITIZER_ENVELOPE_ALWAYS")

	return fields
}

func advancedFields(settings, defaults config.Settings) []configField {
	var fields []configField

	fields = appendBoolField(fields, "compact_responses", settings.CompactResponses, defaults.CompactResponses, "MCPLEXER_COMPACT_RESPONSES")
	fields = appendIntField(fields, "tools_cache_ttl_sec", settings.ToolsCacheTTLSec, defaults.ToolsCacheTTLSec, "MCPLEXER_TOOLS_CACHE_TTL_SEC")
	fields = appendIntField(fields, "code_mode_timeout_sec", settings.CodeModeTimeoutSec, defaults.CodeModeTimeoutSec, "MCPLEXER_CODE_MODE_TIMEOUT_SEC")
	fields = appendIntField(fields, "code_mode_max_output_bytes", settings.CodeModeMaxOutputBytes, defaults.CodeModeMaxOutputBytes, "MCPLEXER_CODE_MODE_MAX_OUTPUT_BYTES")

	fields = append(fields, configField{
		name:   "description_refinement_mode",
		value:  settings.DescriptionRefinementMode,
		source: envSource("MCPLEXER_DESCRIPTION_REFINEMENT_MODE", settings.DescriptionRefinementMode, defaults.DescriptionRefinementMode),
	})

	fields = append(fields, configField{
		name:   "display_name",
		value:  settings.DisplayName,
		source: envSource("MCPLEXER_DISPLAY_NAME", settings.DisplayName, defaults.DisplayName),
	})

	if len(settings.DelegationDisabledProviders) > 0 {
		keys := make([]string, 0, len(settings.DelegationDisabledProviders))
		for k, v := range settings.DelegationDisabledProviders {
			if v {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		data, _ := json.Marshal(keys)
		fields = append(fields, configField{"delegation_disabled_providers", string(data), "db"})
	}

	return fields
}

func appendBoolField(fields []configField, name string, val, defaultVal bool, envKey string) []configField {
	s := "false"
	if val {
		s = "true"
	}
	def := "false"
	if defaultVal {
		def = "true"
	}
	source := "default"
	if os.Getenv(envKey) != "" {
		source = "env: " + envKey
	} else if s != def {
		source = "db"
	}
	return append(fields, configField{name, s, source})
}

func appendIntField(fields []configField, name string, val, defaultVal int, envKey string) []configField {
	s := fmt.Sprintf("%d", val)
	source := "default"
	if os.Getenv(envKey) != "" {
		source = "env: " + envKey
	} else if val != defaultVal {
		source = "db"
	}
	return append(fields, configField{name, s, source})
}

func envSource(envKey, current, defaultVal string) string {
	if os.Getenv(envKey) != "" {
		return "env: " + envKey
	}
	if current != defaultVal {
		return "db"
	}
	return "default"
}
