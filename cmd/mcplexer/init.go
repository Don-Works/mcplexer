package main

import (
	"context"
	"fmt"
	"os"

	"github.com/don-works/mcplexer/internal/install"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func cmdInit() error {
	ctx := context.Background()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Create database
	db, err := sqlite.New(ctx, cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	_ = db.Close()
	fmt.Printf("Database created: %s\n", cfg.DBDSN)

	// Create default config if not exists
	if _, err := os.Stat(cfg.ConfigFile); os.IsNotExist(err) {
		defaultCfg := `# MCPlexer Configuration
# OAuth providers are seeded from built-in templates on first startup.
# Configure workspaces, servers, and routes via the web UI.

oauth_providers: []
workspaces: []
auth_scopes: []
downstream_servers: []
route_rules: []
`
		if err := install.AtomicWriteFile(cfg.ConfigFile, []byte(defaultCfg), 0o644); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		fmt.Printf("Config file created: %s\n", cfg.ConfigFile)
	} else {
		fmt.Printf("Config file already exists: %s\n", cfg.ConfigFile)
	}

	return nil
}
