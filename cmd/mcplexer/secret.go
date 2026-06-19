package main

import (
	"context"
	"fmt"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func cmdSecret(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: mcplexer secret <put|get|list|delete> [args...]")
	}

	ctx := context.Background()
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.AgeKeyPath == "" {
		return fmt.Errorf("MCPLEXER_AGE_KEY must be set to manage secrets")
	}

	db, err := sqlite.New(ctx, cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	enc, err := secrets.NewAgeEncryptor(cfg.AgeKeyPath)
	if err != nil {
		return fmt.Errorf("create encryptor: %w", err)
	}
	sm := secrets.NewManager(db, enc)
	// Audit every CLI-driven secret operation. Same audit_records table
	// the daemon writes to, so the forensics trail includes operator
	// terminal access — `mcplexer secret get $key` is just as worth
	// recording as an agent-side Get.
	sm.SetAuditor(audit.NewLogger(db, db, nil))

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "put":
		if len(rest) < 3 {
			return fmt.Errorf("usage: mcplexer secret put <scope-id> <key> <value>")
		}
		if err := sm.Put(ctx, rest[0], rest[1], []byte(rest[2])); err != nil {
			return fmt.Errorf("put secret: %w", err)
		}
		fmt.Printf("Secret %q set on auth scope %q\n", rest[1], rest[0])

	case "get":
		if len(rest) < 2 {
			return fmt.Errorf("usage: mcplexer secret get <scope-id> <key>")
		}
		val, err := sm.Get(ctx, rest[0], rest[1])
		if err != nil {
			return fmt.Errorf("get secret: %w", err)
		}
		fmt.Print(string(val))

	case "list":
		if len(rest) < 1 {
			return fmt.Errorf("usage: mcplexer secret list <scope-id>")
		}
		keys, err := sm.List(ctx, rest[0])
		if err != nil {
			return fmt.Errorf("list secrets: %w", err)
		}
		for _, k := range keys {
			fmt.Println(k)
		}

	case "delete":
		if len(rest) < 2 {
			return fmt.Errorf("usage: mcplexer secret delete <scope-id> <key>")
		}
		if err := sm.Delete(ctx, rest[0], rest[1]); err != nil {
			return fmt.Errorf("delete secret: %w", err)
		}
		fmt.Printf("Secret %q deleted from auth scope %q\n", rest[1], rest[0])

	default:
		return fmt.Errorf("unknown secret command: %s\nUsage: mcplexer secret <put|get|list|delete>", sub)
	}

	return nil
}
