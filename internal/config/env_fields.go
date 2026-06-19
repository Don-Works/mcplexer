package config

import "sync"

// EnvField describes a required environment variable for an auth scope.
type EnvField struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Secret bool   `json:"secret"` // true = mask input (passwords, secrets)
}

var (
	envFieldsMu sync.RWMutex
	envFields   = map[string][]EnvField{}
)

// RegisterEnvFields registers required env fields for an auth scope ID.
func RegisterEnvFields(scopeID string, fields []EnvField) {
	envFieldsMu.Lock()
	defer envFieldsMu.Unlock()
	envFields[scopeID] = fields
}

// GetEnvFields returns the registered env fields for a scope, or nil.
func GetEnvFields(scopeID string) []EnvField {
	envFieldsMu.RLock()
	defer envFieldsMu.RUnlock()
	return envFields[scopeID]
}
