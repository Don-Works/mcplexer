package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ScopeExtractor extracts resource identifiers from tool call arguments.
// Each downstream server type registers one to enable scope policy enforcement.
type ScopeExtractor interface {
	// Extract returns a map of resource_type -> set of values found in args.
	// For example, a GitHub extractor returns {"org": {"acme"}, "repo": {"acme/api"}}.
	Extract(args json.RawMessage) map[string]map[string]struct{}
}

// ScopeRegistry maps tool namespace prefixes to extractors.
type ScopeRegistry struct {
	extractors map[string]ScopeExtractor
}

// NewScopeRegistry creates an empty scope registry.
func NewScopeRegistry() *ScopeRegistry {
	return &ScopeRegistry{
		extractors: make(map[string]ScopeExtractor),
	}
}

// Register adds an extractor for the given namespace prefix.
func (r *ScopeRegistry) Register(namespace string, e ScopeExtractor) {
	r.extractors[namespace] = e
}

// Get returns the extractor for the given namespaced tool name, or nil.
// It matches on the namespace prefix before the first "__" separator.
func (r *ScopeRegistry) Get(toolName string) ScopeExtractor {
	ns, _, ok := strings.Cut(toolName, "__")
	if !ok {
		return nil
	}
	return r.extractors[ns]
}

// ScopePolicy parses and enforces generic resource allowlists from a route rule.
//
// The policy is a JSON object mapping resource types to allowed values:
//
//	{"org": ["acme"], "repo": ["acme/api", "acme/web"]}
//
// An empty or nil policy means no enforcement (permissive default).
type ScopePolicy struct {
	constraints map[string]map[string]struct{}
}

// NewScopePolicy parses a scope_policy JSON field from a route rule.
func NewScopePolicy(raw json.RawMessage) (*ScopePolicy, error) {
	p := &ScopePolicy{
		constraints: make(map[string]map[string]struct{}),
	}
	if len(raw) == 0 || string(raw) == "{}" || string(raw) == "null" {
		return p, nil
	}

	var parsed map[string][]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("scope_policy: must be a JSON object with string array values: %w", err)
	}

	for resourceType, values := range parsed {
		set := make(map[string]struct{}, len(values))
		for _, v := range values {
			v = strings.ToLower(strings.TrimSpace(v))
			if v == "" {
				continue
			}
			set[v] = struct{}{}
		}
		if len(set) > 0 {
			p.constraints[resourceType] = set
		}
	}
	return p, nil
}

// Enabled returns true if the policy has any constraints configured.
func (p *ScopePolicy) Enabled() bool {
	return len(p.constraints) > 0
}

// Enforce checks extracted resource identifiers against the allowlists.
// For each resource type in the extracted map, if the policy has a constraint
// for that type, every extracted value must be in the allowlist. Resource types
// without constraints are unconstrained (pass-through).
func (p *ScopePolicy) Enforce(extracted map[string]map[string]struct{}) error {
	for resourceType, allowed := range p.constraints {
		found, ok := extracted[resourceType]
		if !ok || len(found) == 0 {
			continue
		}
		for value := range found {
			if _, permitted := allowed[value]; !permitted {
				return &ScopePolicyViolation{
					ResourceType: resourceType,
					Value:        value,
					Allowed:      allowed,
				}
			}
		}
	}
	return nil
}

// ScopePolicyViolation is returned when a tool call targets a resource
// not permitted by the route's scope policy.
type ScopePolicyViolation struct {
	ResourceType string
	Value        string
	Allowed      map[string]struct{}
}

func (v *ScopePolicyViolation) Error() string {
	allowed := make([]string, 0, len(v.Allowed))
	for a := range v.Allowed {
		allowed = append(allowed, a)
	}
	return fmt.Sprintf(
		"%s %q is not allowed by the route's scope policy (allowed: %s)",
		v.ResourceType, v.Value, strings.Join(allowed, ", "),
	)
}

// ValidateScopePolicy validates that a scope_policy field is well-formed.
func ValidateScopePolicy(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var parsed map[string][]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("scope_policy: must be a JSON object with string array values")
	}
	for key, values := range parsed {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("scope_policy: empty resource type key not allowed")
		}
		for i, v := range values {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("scope_policy.%s[%d]: empty string not allowed", key, i)
			}
		}
	}
	return nil
}
