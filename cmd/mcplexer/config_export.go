package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"gopkg.in/yaml.v3"
)

func cmdConfigExport(args []string) error {
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

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("unmarshal settings: %w", err)
	}

	stripSecretsFromMap(raw)

	out, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}

	fmt.Print(string(out))
	return nil
}

func stripSecretsFromMap(raw map[string]any) {
	secretKeys := []string{"api_key", "api_keys", "oauth_token", "access_token", "refresh_token", "client_secret", "secret_key", "secret_keys"}
	for _, k := range secretKeys {
		delete(raw, k)
	}
	for key := range raw {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") {
			delete(raw, key)
		}
	}
}
