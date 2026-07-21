package addon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// builderResolver returns a NamespaceResolver that uses the spec's namespace
// directly as the parent_server's namespace, mirroring how seeded addons
// resolve in production.
func builderResolver(serverID, ns string) NamespaceResolver {
	return func(id string) (string, error) {
		if id == serverID {
			return ns, nil
		}
		return "", &resolverError{id}
	}
}

type resolverError struct{ id string }

func (e *resolverError) Error() string { return "unknown server " + e.id }

func TestBuildAddonYAML_RoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		spec      AddonSpec
		ns        string
		wantTools int
		wantSubs  []string // substrings expected in generated YAML
	}{
		{
			name: "bearer token",
			spec: AddonSpec{
				Name:         "weatherco",
				Description:  "Public weather API",
				BaseURL:      "https://api.weather.co/v1",
				ParentServer: "weatherco-server",
				Auth:         AuthSpec{Kind: AuthBearer},
				Endpoints: []EndpointSpec{
					{
						Name: "get_forecast", Description: "Get a 7-day forecast",
						Method: "GET", Path: "/forecast/{{city}}",
						Params: []ParamSpec{
							{Name: "city", Type: "string", In: "path", Required: true},
							{Name: "units", Type: "string", In: "query"},
						},
					},
				},
			},
			ns:        "weatherco",
			wantTools: 1,
			wantSubs: []string{
				"parent_server: weatherco-server",
				"name: get_forecast",
				"method: GET",
				"https://api.weather.co/v1/forecast/{{city}}",
				"units: '{{units}}'",
			},
		},
		{
			name: "api_key_header",
			spec: AddonSpec{
				Name:         "stockapi",
				Description:  "Quotes and trades",
				BaseURL:      "https://api.stock.example/",
				ParentServer: "stock-server",
				Auth:         AuthSpec{Kind: AuthAPIKeyHeader, HeaderName: "X-Api-Key"},
				Endpoints: []EndpointSpec{
					{
						Name: "get_quote", Description: "Latest quote",
						Method: "GET", Path: "/quote",
						Params: []ParamSpec{
							{Name: "symbol", Type: "string", In: "query", Required: true},
						},
					},
					{
						Name: "place_order", Description: "Place a new order",
						Method: "POST", Path: "/orders",
						Params: []ParamSpec{
							{Name: "symbol", Type: "string", In: "body", Required: true},
							{Name: "qty", Type: "integer", In: "body", Required: true},
						},
					},
				},
			},
			ns:        "stockapi",
			wantTools: 2,
			wantSubs: []string{
				"X-Api-Key: '{{_api_key}}'",
				"name: place_order",
				"method: POST",
			},
		},
		{
			name: "no_auth",
			spec: AddonSpec{
				Name:         "publicapi",
				Description:  "Read-only public API",
				BaseURL:      "https://api.public.example",
				ParentServer: "public-server",
				Auth:         AuthSpec{Kind: AuthNone},
				Endpoints: []EndpointSpec{
					{
						Name: "list_items", Description: "List items",
						Method: "GET", Path: "/items",
					},
				},
			},
			ns:        "publicapi",
			wantTools: 1,
			wantSubs: []string{
				"parent_server: public-server",
				"name: list_items",
			},
		},
		{
			name: "hawk",
			spec: AddonSpec{
				Name:         "absence",
				Description:  "Absence API",
				BaseURL:      "https://app.absence.io/api/v2",
				ParentServer: "absence-server",
				Auth:         AuthSpec{Kind: AuthHawk},
				Endpoints: []EndpointSpec{
					{
						Name: "list_users", Description: "List users",
						Method: "GET", Path: "/users",
					},
				},
			},
			ns:        "absence",
			wantTools: 1,
			wantSubs: []string{
				"# Auth: hawk",
				"parent_server: absence-server",
				"name: list_users",
			},
		},
		{
			// Regression: agents using mcpx__create_addon (or the create-mcp
			// wizard) typically write path params in OpenAPI's `{name}` form,
			// not the executor's `{{slug}}` form. The builder must rewrite
			// them at YAML-bake time so calls don't ship literal `{id}` to
			// the upstream API. Verified live against api.freeagent.com.
			name: "single_brace_path_params_rewritten",
			spec: AddonSpec{
				Name:         "freeagentlike",
				Description:  "Single-brace path placeholders should be rewritten",
				BaseURL:      "https://api.freeagent.com/v2",
				ParentServer: "freeagent-server",
				Auth:         AuthSpec{Kind: AuthBearer},
				Endpoints: []EndpointSpec{
					{
						Name: "get_invoice", Description: "Get an invoice",
						Method: "GET", Path: "/invoices/{id}",
						Params: []ParamSpec{{Name: "id", Type: "integer", In: "path", Required: true}},
					},
				},
			},
			ns:        "freeagentlike",
			wantTools: 1,
			wantSubs: []string{
				"https://api.freeagent.com/v2/invoices/{{id}}",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yamlText, err := BuildAddonYAML(tt.spec)
			if err != nil {
				t.Fatalf("BuildAddonYAML: %v", err)
			}
			for _, sub := range tt.wantSubs {
				if !strings.Contains(yamlText, sub) {
					t.Errorf("yaml missing substring %q\n--- yaml ---\n%s", sub, yamlText)
				}
			}

			// Round-trip: write to a temp dir and load it.
			dir := t.TempDir()
			path := filepath.Join(dir, tt.spec.Name+".yaml")
			if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
				t.Fatal(err)
			}
			parentServerID := tt.spec.ParentServer
			reg, err := LoadDir(dir, builderResolver(parentServerID, tt.ns))
			if err != nil {
				t.Fatalf("LoadDir: %v", err)
			}
			if got := len(reg.AllTools()); got != tt.wantTools {
				t.Errorf("loaded %d tools, want %d", got, tt.wantTools)
			}
			for _, ep := range tt.spec.Endpoints {
				full := tt.ns + "__" + ep.Name
				if reg.GetTool(full) == nil {
					t.Errorf("expected tool %q to be registered", full)
				}
			}
		})
	}
}

func TestAddonSpec_Validate_RejectsInvalid(t *testing.T) {
	good := func() AddonSpec {
		return AddonSpec{
			Name: "ok", Description: "ok", BaseURL: "https://x",
			ParentServer: "srv", Auth: AuthSpec{Kind: AuthNone},
			Endpoints: []EndpointSpec{{
				Name: "ep", Description: "d", Method: "GET", Path: "/p",
			}},
		}
	}
	tests := []struct {
		name    string
		mutate  func(*AddonSpec)
		wantSub string
	}{
		{"empty name", func(s *AddonSpec) { s.Name = "" }, "name must match"},
		{"bad name", func(s *AddonSpec) { s.Name = "Has-Caps" }, "name must match"},
		{"no description", func(s *AddonSpec) { s.Description = "" }, "description is required"},
		{"bad url", func(s *AddonSpec) { s.BaseURL = "ftp://x" }, "base_url"},
		{"no parent", func(s *AddonSpec) { s.ParentServer = "" }, "parent_server"},
		{"bad auth", func(s *AddonSpec) { s.Auth = AuthSpec{Kind: AuthKind("xx")} }, "auth.kind"},
		{
			"missing header name",
			func(s *AddonSpec) { s.Auth = AuthSpec{Kind: AuthAPIKeyHeader} },
			"header_name",
		},
		{
			"missing query name",
			func(s *AddonSpec) { s.Auth = AuthSpec{Kind: AuthAPIKeyQuery} },
			"query_name",
		},
		{"no endpoints", func(s *AddonSpec) { s.Endpoints = nil }, "at least one endpoint"},
		{
			"bad method",
			func(s *AddonSpec) { s.Endpoints[0].Method = "FETCH" },
			"method",
		},
		{
			"bad path",
			func(s *AddonSpec) { s.Endpoints[0].Path = "no-slash" },
			"path must start",
		},
		{
			"bad param type",
			func(s *AddonSpec) {
				s.Endpoints[0].Params = []ParamSpec{{Name: "foo", Type: "blob", In: "query"}}
			},
			"params[0].type",
		},
		{
			"bad param in",
			func(s *AddonSpec) {
				s.Endpoints[0].Params = []ParamSpec{{Name: "foo", Type: "string", In: "header"}}
			},
			"params[0].in",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := good()
			tt.mutate(&s)
			err := s.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want contains %q", err, tt.wantSub)
			}
			if _, err := BuildAddonYAML(s); err == nil {
				t.Errorf("BuildAddonYAML accepted invalid spec")
			}
		})
	}
}

func TestWriteAndRegister_HotReload(t *testing.T) {
	dir := t.TempDir()
	// Start with an empty registry.
	reg, err := LoadDir(dir, builderResolver("any-server", "any"))
	if err != nil {
		t.Fatalf("initial LoadDir: %v", err)
	}
	if got := len(reg.AllTools()); got != 0 {
		t.Fatalf("expected empty registry, got %d", got)
	}

	spec := AddonSpec{
		Name: "scratchpad", Description: "Test", BaseURL: "https://x",
		ParentServer: "scratch-srv", Auth: AuthSpec{Kind: AuthNone},
		Endpoints: []EndpointSpec{{
			Name: "ping", Description: "ping", Method: "GET", Path: "/ping",
		}},
	}

	path, err := WriteAndRegister(reg, dir, spec, builderResolver("scratch-srv", "scratchpad"))
	if err != nil {
		t.Fatalf("WriteAndRegister: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}
	if got := len(reg.AllTools()); got != 1 {
		t.Errorf("after register: got %d tools, want 1", got)
	}
	if reg.GetTool("scratchpad__ping") == nil {
		t.Errorf("expected scratchpad__ping to be registered")
	}
}
