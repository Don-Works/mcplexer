package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const timeFormat = time.RFC3339

// parseTimeFormats covers the canonical RFC3339 we write via formatTime plus
// SQLite's CURRENT_TIMESTAMP default ("YYYY-MM-DD HH:MM:SS", UTC, no zone)
// which shows up on rows populated by DEFAULT CURRENT_TIMESTAMP at insert time.
var parseTimeFormats = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

func formatTime(t time.Time) string {
	return t.UTC().Format(timeFormat)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, f := range parseTimeFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	slog.Warn("parseTime failed", "input", s, "tried", parseTimeFormats)
	return time.Time{}
}

func parseTimePtr(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t := parseTime(*s)
	return &t
}

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatTime(*t)
	return &s
}

func normalizeJSON(data json.RawMessage, fallback string) string {
	if len(data) == 0 {
		return fallback
	}
	return string(data)
}

func checkRowsAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// nullString returns sql.NullString{Valid: false} for an empty string and
// {Valid: true, String: s} otherwise. Use it when binding a nullable FK
// column so an empty Go string becomes a SQL NULL.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func mapConstraintError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "unique_") ||
		strings.Contains(msg, "already exists") {
		return store.ErrAlreadyExists
	}
	return err
}

// validMetaKey checks that a meta key contains only safe characters
// ([a-zA-Z0-9_-]+). This is defense-in-depth: the handler/API layers
// validate keys upstream, but the store layer rejects them independently
// so a key can never reach sql interpolation without passing this check.
func validMetaKey(key string) error {
	if key == "" {
		return fmt.Errorf("meta key is empty")
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return fmt.Errorf("meta key %q contains illegal character %q (allowed: [a-zA-Z0-9_-])", key, r)
		}
	}
	return nil
}
