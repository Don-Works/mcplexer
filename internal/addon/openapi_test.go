package addon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func mustReadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

func TestImportOpenAPI_Petstore(t *testing.T) {
	spec, err := ImportOpenAPI(mustReadTestdata(t, "petstore.yaml"))
	if err != nil {
		t.Fatalf("ImportOpenAPI: %v", err)
	}
	if spec.Name != "swagger_petstore" {
		t.Errorf("name = %q, want swagger_petstore", spec.Name)
	}
	if spec.BaseURL != "https://petstore.swagger.io/v2" {
		t.Errorf("base_url = %q", spec.BaseURL)
	}
	if spec.Auth.Kind != AuthAPIKeyHeader {
		t.Errorf("auth.kind = %q, want api_key_header", spec.Auth.Kind)
	}
	if spec.Auth.HeaderName != "api_key" {
		t.Errorf("auth.header_name = %q, want api_key", spec.Auth.HeaderName)
	}
	wantOps := []string{"addPet", "updatePet", "findPetsByStatus", "getPetById", "deletePet"}
	if len(spec.Endpoints) != len(wantOps) {
		t.Fatalf("endpoints = %d, want %d", len(spec.Endpoints), len(wantOps))
	}
	for _, want := range wantOps {
		if !hasEndpoint(spec.Endpoints, slugifyName(want)) {
			t.Errorf("missing endpoint %q; got %v", want, endpointNames(spec.Endpoints))
		}
	}
	// addPet body params should appear as in=body.
	addPet := findEndpointPtr(spec.Endpoints, "addpet")
	if addPet == nil {
		t.Fatal("addPet endpoint not found")
	}
	if !hasParam(addPet.Params, "name", "body") {
		t.Errorf("addPet missing body param 'name'; got %+v", addPet.Params)
	}
	// findPetsByStatus has a required query param.
	findByStatus := findEndpointPtr(spec.Endpoints, "findpetsbystatus")
	if findByStatus == nil || !hasParam(findByStatus.Params, "status", "query") {
		t.Errorf("findPetsByStatus missing query param 'status'")
	}
}

func TestImportOpenAPI_GitHubBearer(t *testing.T) {
	spec, err := ImportOpenAPI(mustReadTestdata(t, "github.yaml"))
	if err != nil {
		t.Fatalf("ImportOpenAPI: %v", err)
	}
	if spec.Auth.Kind != AuthBearer {
		t.Errorf("auth.kind = %q, want bearer", spec.Auth.Kind)
	}
	if spec.Name != "github" {
		t.Errorf("name = %q, want github", spec.Name)
	}
	// Path parameters must surface as in=path with required=true.
	for _, ep := range spec.Endpoints {
		if strings.Contains(ep.Path, "{owner}") && !hasParam(ep.Params, "owner", "path") {
			t.Errorf("endpoint %q missing path param 'owner'", ep.Name)
		}
	}
	// Names should be slugified — slashes replaced with underscores.
	for _, ep := range spec.Endpoints {
		if strings.ContainsAny(ep.Name, "/-") {
			t.Errorf("endpoint name %q is not slugified", ep.Name)
		}
	}
}

func TestImportOpenAPI_StripeBearer(t *testing.T) {
	spec, err := ImportOpenAPI(mustReadTestdata(t, "stripe.yaml"))
	if err != nil {
		t.Fatalf("ImportOpenAPI: %v", err)
	}
	if spec.Auth.Kind != AuthBearer {
		t.Errorf("Stripe primary security is bearerAuth — got %q", spec.Auth.Kind)
	}
	if !hasEndpoint(spec.Endpoints, "createcharge") {
		t.Errorf("missing CreateCharge endpoint; got %s", endpointNames(spec.Endpoints))
	}
}

func TestImportOpenAPI_Hawk(t *testing.T) {
	spec, err := ImportOpenAPI([]byte(`openapi: 3.0.3
info: {title: Absence, version: '1'}
servers: [{url: 'https://app.absence.io/api/v2'}]
components:
  securitySchemes:
    hawkAuth:
      type: http
      scheme: hawk
security:
  - hawkAuth: []
paths:
  /users:
    get:
      operationId: listUsers
      responses: {'200': {description: ok}}
`))
	if err != nil {
		t.Fatalf("ImportOpenAPI: %v", err)
	}
	if spec.Auth.Kind != AuthHawk {
		t.Errorf("auth.kind = %q, want hawk", spec.Auth.Kind)
	}
}

func TestImportOpenAPI_OAuth2Pending(t *testing.T) {
	spec, err := ImportOpenAPI(mustReadTestdata(t, "oauth2.yaml"))
	if err != nil {
		t.Fatalf("ImportOpenAPI: %v", err)
	}
	if string(spec.Auth.Kind) != "oauth2_pending" {
		t.Errorf("auth.kind = %q, want oauth2_pending", spec.Auth.Kind)
	}
	// Regression: the importer must surface auth_url/token_url/scopes from
	// the spec, or downstream tooling (provision_mcp's wizard hint, the
	// create-mcp wizard UI) has no idea where to send the human.
	if spec.Auth.AuthURL == "" {
		t.Errorf("auth_url is empty — wizard cannot point the human anywhere")
	}
	if spec.Auth.TokenURL == "" {
		t.Errorf("token_url is empty")
	}
	if len(spec.Auth.Scopes) == 0 {
		t.Errorf("scopes are empty — wizard would request no permissions")
	}
}

func TestImportOpenAPI_BasicOnlyRejected(t *testing.T) {
	_, err := ImportOpenAPI(mustReadTestdata(t, "basic_only.yaml"))
	if err == nil {
		t.Fatal("expected an error for HTTP Basic-only spec")
	}
	if !strings.Contains(err.Error(), "Basic") {
		t.Errorf("error should mention Basic, got: %v", err)
	}
}

func TestImportOpenAPI_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		wantSub string
	}{
		{"empty", "", "empty"},
		{"not-openapi", "this is not yaml: [", "parse openapi"},
		{"no-paths", `openapi: 3.0.0
info: {title: t, version: '1'}
servers: [{url: 'https://x'}]
paths: {}
`, "no paths"},
		{"no-server", `openapi: 3.0.0
info: {title: t, version: '1'}
paths:
  /a:
    get: {responses: {'200': {description: ok}}}
`, "servers[0].url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ImportOpenAPI([]byte(tc.spec))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestImportOpenAPI_RewritesPathPlaceholders(t *testing.T) {
	// Regression: the OpenAPI importer used to leave OpenAPI's `{petId}`
	// placeholders in the generated path while slugifying the input-schema
	// name to `petid`. The addon executor expects `{{slug}}` placeholders, so
	// calls would send the literal `{petId}` to the upstream API and 400.
	spec, err := ImportOpenAPI(mustReadTestdata(t, "petstore.yaml"))
	if err != nil {
		t.Fatalf("ImportOpenAPI: %v", err)
	}
	getById := findEndpointPtr(spec.Endpoints, "getpetbyid")
	if getById == nil {
		t.Fatal("getPetById endpoint not found")
	}
	if !strings.Contains(getById.Path, "{{petid}}") {
		t.Errorf("path = %q, want it to contain {{petid}} (placeholder rewritten for executor)", getById.Path)
	}
	if strings.Contains(getById.Path, "{petId}") {
		t.Errorf("path = %q, still contains literal OpenAPI placeholder {petId}", getById.Path)
	}
}

// TestImportOpenAPI_RoundTripBuildsValidYAML verifies that a produced
// AddonSpec, after the user fills in parent_server, can be turned into YAML
// by BuildAddonYAML and round-trips back through the addon loader.
func TestImportOpenAPI_RoundTripBuildsValidYAML(t *testing.T) {
	spec, err := ImportOpenAPI(mustReadTestdata(t, "petstore.yaml"))
	if err != nil {
		t.Fatalf("ImportOpenAPI: %v", err)
	}
	// User fills in parent_server in the wizard.
	spec.ParentServer = "petstore"

	yamlText, err := BuildAddonYAML(*spec)
	if err != nil {
		t.Fatalf("BuildAddonYAML: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, spec.Name+".yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	// Resolve parent_server to a namespace deterministically.
	resolve := func(id string) (string, error) { return spec.Name, nil }
	reg, err := LoadDir(dir, resolve)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if got := len(reg.AllTools()); got != len(spec.Endpoints) {
		t.Errorf("loaded %d tools, want %d", got, len(spec.Endpoints))
	}

	// The generated YAML should also unmarshal to AddonFile cleanly.
	var af AddonFile
	if err := yaml.Unmarshal([]byte(yamlText), &af); err != nil {
		t.Fatalf("unmarshal generated yaml: %v", err)
	}
}

func TestSlugifyName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Swagger Petstore", "swagger_petstore"},
		{"GitHub", "github"},
		{"users/get-authenticated", "users_get_authenticated"},
		{"123abc", "x_123abc"},
		{"!!!", ""},
		{strings.Repeat("a", 80), strings.Repeat("a", 62)},
	}
	for _, c := range cases {
		if got := slugifyName(c.in); got != c.want {
			t.Errorf("slugifyName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func hasParam(params []ParamSpec, name, in string) bool {
	for _, p := range params {
		if p.Name == name && p.In == in {
			return true
		}
	}
	return false
}

func findEndpointPtr(eps []EndpointSpec, name string) *EndpointSpec {
	for i := range eps {
		if eps[i].Name == name {
			return &eps[i]
		}
	}
	return nil
}

func hasEndpoint(eps []EndpointSpec, name string) bool {
	return findEndpointPtr(eps, name) != nil
}

func endpointNames(eps []EndpointSpec) []string {
	out := make([]string, len(eps))
	for i, e := range eps {
		out[i] = e.Name
	}
	return out
}
