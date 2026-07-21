package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func collaborationTime(at time.Time) time.Time {
	if at.IsZero() {
		at = time.Now()
	}
	return at.UTC().Truncate(time.Second)
}

func nullableUnix(at *time.Time) any {
	if at == nil {
		return nil
	}
	return collaborationTime(*at).Unix()
}

func unixTime(value int64) time.Time {
	return time.Unix(value, 0).UTC()
}

func unixTimePtr(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	at := unixTime(value.Int64)
	return &at
}

// collaborationJSON returns a canonical JSON object. Arrays, scalars, trailing
// input, and an explicit JSON null are rejected so policy constraints have one
// unambiguous representation at every authorization boundary.
func collaborationJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, fmt.Errorf("constraints must be a JSON object: %w", err)
	}
	if object == nil {
		return nil, fmt.Errorf("constraints must be a JSON object")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("marshal constraints: %w", err)
	}
	return canonical, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("constraints contain trailing JSON")
		}
		return fmt.Errorf("decode trailing constraints: %w", err)
	}
	return nil
}

func validPrincipalKind(kind string) bool {
	return kind == store.PrincipalKindPerson || kind == store.PrincipalKindMachine
}

func validPrincipalStatus(status string) bool {
	switch status {
	case store.PrincipalStatusPending, store.PrincipalStatusActive,
		store.PrincipalStatusLegacyUnverified, store.PrincipalStatusRevoked:
		return true
	default:
		return false
	}
}

func validPrincipalKeyStatus(status string) bool {
	switch status {
	case store.PrincipalKeyStatusPending, store.PrincipalKeyStatusActive,
		store.PrincipalKeyStatusRevoked:
		return true
	default:
		return false
	}
}

func validDeviceKind(kind string) bool {
	switch kind {
	case "laptop", "server", "daemon", "unknown":
		return true
	default:
		return false
	}
}

func validInvitationPurpose(purpose string) bool {
	switch purpose {
	case store.InvitationPurposeNewPrincipal, store.InvitationPurposeAddDevice,
		store.InvitationPurposeRotateKey:
		return true
	default:
		return false
	}
}

func requiredLabel(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("%s is invalid", field)
	}
	return value, nil
}
