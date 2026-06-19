package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"gopkg.in/yaml.v3"
)

func cmdConfigImport(args []string) error {
	if len(args) < 1 || args[0] == "" {
		return fmt.Errorf("usage: mcplexer config import <file>")
	}
	filePath := args[0]

	ctx := context.Background()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	rawYAML, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(rawYAML, &raw); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}

	stripSecretsFromMap(raw)

	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}

	var incoming config.Settings
	if err := json.Unmarshal(jsonBytes, &incoming); err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}

	defaults := config.DefaultSettings()
	merged := mergeImportedSettings(defaults, incoming)

	db, err := sqlite.New(ctx, cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	settingsSvc := config.NewSettingsService(db)
	prev := settingsSvc.Load(ctx)

	diff := buildImportDiff(prev, merged)

	if len(diff) == 0 {
		fmt.Println("No changes to apply.")
		return nil
	}

	fmt.Println("Changes to apply:")
	fmt.Println("━━━━━━━━━━━━━━━━━")
	for _, d := range diff {
		fmt.Printf("  %-35s %v → %v\n", d.field+":", d.before, d.after)
	}
	fmt.Println()

	if err := settingsSvc.Save(ctx, merged); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	fmt.Println("Settings imported successfully.")
	return nil
}

type importDiffEntry struct {
	field  string
	before any
	after  any
}

func mergeImportedSettings(defaults, incoming config.Settings) config.Settings {
	merged := defaults

	if incoming.LogLevel != "" {
		merged.LogLevel = incoming.LogLevel
	}
	if incoming.DisplayName != "" {
		merged.DisplayName = incoming.DisplayName
	}
	if incoming.DescriptionRefinementMode != "" {
		merged.DescriptionRefinementMode = incoming.DescriptionRefinementMode
	}
	if incoming.RemoteSkillServerURL != "" {
		merged.RemoteSkillServerURL = incoming.RemoteSkillServerURL
	}

	if incoming.SlimTools {
		merged.SlimTools = incoming.SlimTools
	}
	if incoming.SlimSurface {
		merged.SlimSurface = incoming.SlimSurface
	}
	if incoming.CompactResponses {
		merged.CompactResponses = incoming.CompactResponses
	}
	if incoming.ToolsCacheTTLSec != 0 {
		merged.ToolsCacheTTLSec = incoming.ToolsCacheTTLSec
	}
	if incoming.CodeModeTimeoutSec != 0 {
		merged.CodeModeTimeoutSec = incoming.CodeModeTimeoutSec
	}
	if incoming.CodeModeMaxOutputBytes != 0 {
		merged.CodeModeMaxOutputBytes = incoming.CodeModeMaxOutputBytes
	}
	if incoming.MeshEnabled {
		merged.MeshEnabled = incoming.MeshEnabled
	}
	if incoming.MeshReceiveMaxResults != 0 {
		merged.MeshReceiveMaxResults = incoming.MeshReceiveMaxResults
	}
	if incoming.MeshReceivePreviewBytes != 0 {
		merged.MeshReceivePreviewBytes = incoming.MeshReceivePreviewBytes
	}
	if incoming.MeshSendMaxContentBytes != 0 {
		merged.MeshSendMaxContentBytes = incoming.MeshSendMaxContentBytes
	}
	if incoming.P2PEnabled {
		merged.P2PEnabled = incoming.P2PEnabled
	}
	if incoming.SanitizerEnvelopeAlways {
		merged.SanitizerEnvelopeAlways = incoming.SanitizerEnvelopeAlways
	}
	if incoming.SandboxDownstreams {
		merged.SandboxDownstreams = incoming.SandboxDownstreams
	}
	if incoming.TelegramEnabled {
		merged.TelegramEnabled = incoming.TelegramEnabled
	}
	if incoming.DangerousModeEnabled {
		merged.DangerousModeEnabled = incoming.DangerousModeEnabled
	}
	if incoming.MeshAutoReplicateOff {
		merged.MeshAutoReplicateOff = incoming.MeshAutoReplicateOff
	}

	if incoming.ToolDescriptionOverrides != nil {
		merged.ToolDescriptionOverrides = incoming.ToolDescriptionOverrides
	}
	if incoming.ToolHints != nil {
		merged.ToolHints = incoming.ToolHints
	}
	if incoming.DelegationDisabledProviders != nil {
		merged.DelegationDisabledProviders = incoming.DelegationDisabledProviders
	}

	return merged
}

func buildImportDiff(prev, next config.Settings) []importDiffEntry {
	var diff []importDiffEntry

	prevJSON, _ := json.Marshal(prev)
	nextJSON, _ := json.Marshal(next)

	var prevMap, nextMap map[string]any
	_ = json.Unmarshal(prevJSON, &prevMap)
	_ = json.Unmarshal(nextJSON, &nextMap)

	for key, nextVal := range nextMap {
		prevVal, existed := prevMap[key]
		if !existed {
			diff = append(diff, importDiffEntry{field: key, before: nil, after: nextVal})
			continue
		}
		pj, _ := json.Marshal(prevVal)
		nj, _ := json.Marshal(nextVal)
		if string(pj) != string(nj) {
			diff = append(diff, importDiffEntry{field: key, before: prevVal, after: nextVal})
		}
	}

	return diff
}
