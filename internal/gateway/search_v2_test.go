package gateway

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// fixtureTools builds a representative tool catalog used by ranker tests.
// It mixes common namespaces (github, slack, calendar, customer, linear,
// freeagent) plus a few near-duplicates so ranking actually has to discriminate.
func fixtureTools() []Tool {
	return []Tool{
		{Name: "github__list_issues", Description: "List GitHub issues."},
		{Name: "github__create_issue", Description: "Create a new GitHub issue."},
		{Name: "github__list_repos", Description: "List repositories for a user."},

		{Name: "slack__send_message", Description: "Send a chat message to a Slack channel."},
		{Name: "slack__list_channels", Description: "List Slack channels."},

		{Name: "calendar__schedule_event", Description: "Schedule a calendar event or meeting."},

		{Name: "customer__create_customer", Description: "Add a new customer."},
		{Name: "customer__get_customer_snapshot", Description: "Fetch a customer's full snapshot."},
		{Name: "customer__list_invoices", Description: "List invoices for a customer."},
		{Name: "customer__update_invoice", Description: "Update an existing invoice."},

		{Name: "linear__create_issue", Description: "Create a Linear ticket."},
		{Name: "linear__list_tasks", Description: "List Linear tasks for a project."},

		{Name: "freeagent__add_contact", Description: "Add a FreeAgent contact (customer)."},
		{Name: "freeagent__raise_invoice", Description: "Raise a draft invoice in FreeAgent."},
	}
}

// intentCases asserts that for a natural-language intent, the right tool
// shows up in the top 3 of the ranked output. This is the headline metric:
// "given a vague goal, can the agent find the tool?"
func TestHybridSearch_IntentRanking(t *testing.T) {
	cases := []struct {
		intent  string
		wantTop string
	}{
		{"I need to add a customer", "customer__create_customer"},
		{"raise a new invoice for a customer", "freeagent__raise_invoice"},
		{"send a chat message in slack", "slack__send_message"},
		{"schedule a meeting", "calendar__schedule_event"},
		{"create a github issue", "github__create_issue"},
		{"list outstanding invoices for a customer", "customer__list_invoices"},
		{"linear tasks I have to do", "linear__list_tasks"},
	}

	idx := &semanticIndex{}
	tools := fixtureTools()
	idx.rebuild(tools)

	for _, tc := range cases {
		t.Run(tc.intent, func(t *testing.T) {
			ranked := hybridSearch(tools, tc.intent, idx, 5)
			if len(ranked) == 0 {
				t.Fatalf("no results for intent %q", tc.intent)
			}
			if !inTopN(ranked, tc.wantTop, 3) {
				names := make([]string, 0, len(ranked))
				for _, r := range ranked {
					names = append(names, r.Tool.Name)
				}
				t.Errorf("expected %q in top 3 for %q, got %v",
					tc.wantTop, tc.intent, names)
			}
		})
	}
}

// TestHybridSearch_SynonymExpansion verifies that "create" finds tools whose
// name contains a synonym verb such as "make", "add", "raise".
func TestHybridSearch_SynonymExpansion(t *testing.T) {
	idx := &semanticIndex{}
	tools := fixtureTools()
	idx.rebuild(tools)

	// Query "create customer" should surface either customer__create_customer
	// or freeagent__add_contact — both are creation-flavoured customer tools.
	ranked := hybridSearch(tools, "create customer", idx, 5)
	if len(ranked) == 0 {
		t.Fatal("expected results for 'create customer'")
	}
	if !inTopN(ranked, "customer__create_customer", 2) &&
		!inTopN(ranked, "freeagent__add_contact", 2) {
		names := topNames(ranked, 5)
		t.Errorf("synonym cluster failed: 'create customer' top-5 = %v", names)
	}

	// Query "make customer" — pure synonym test; "make" is in the create cluster.
	ranked = hybridSearch(tools, "make customer", idx, 5)
	if len(ranked) == 0 {
		t.Fatal("expected results for 'make customer'")
	}
	if !inTopN(ranked, "customer__create_customer", 3) {
		names := topNames(ranked, 5)
		t.Errorf("'make customer' should rank create_customer in top 3, got %v", names)
	}
}

// TestFilterByNamespaces ensures the namespaces filter restricts the candidate pool.
func TestFilterByNamespaces(t *testing.T) {
	tools := fixtureTools()
	got := filterByNamespaces(tools, []string{"github", "slack"})
	if len(got) == 0 {
		t.Fatal("expected results")
	}
	for _, tool := range got {
		ns, _, _ := splitNamespace(tool.Name)
		if ns != "github" && ns != "slack" {
			t.Errorf("unexpected namespace in filtered set: %s", tool.Name)
		}
	}

	// Empty allow-list returns the input unchanged.
	all := filterByNamespaces(tools, nil)
	if len(all) != len(tools) {
		t.Errorf("nil namespaces should be identity, got %d/%d", len(all), len(tools))
	}
}

// TestHybridSearch_NamespaceFilterIntegration combines filtering with ranking.
func TestHybridSearch_NamespaceFilterIntegration(t *testing.T) {
	tools := fixtureTools()
	scoped := filterByNamespaces(tools, []string{"freeagent"})

	idx := &semanticIndex{}
	idx.rebuild(scoped)

	ranked := hybridSearch(scoped, "create invoice", idx, 5)
	if len(ranked) == 0 {
		t.Fatal("expected freeagent results for 'create invoice'")
	}
	for _, r := range ranked {
		ns, _, _ := splitNamespace(r.Tool.Name)
		if ns != "freeagent" {
			t.Errorf("namespace filter leaked: %s", r.Tool.Name)
		}
	}
	if !inTopN(ranked, "freeagent__raise_invoice", 1) {
		t.Errorf("expected raise_invoice top-1, got %v", topNames(ranked, 3))
	}
}

// TestSearchToolsDefinition_BackwardCompat ensures the existing call shape
// (`{queries: ["..."]}`) still resolves and the schema still advertises queries.
func TestSearchToolsDefinition_BackwardCompat(t *testing.T) {
	def := searchToolsDefinition()
	if def.Name != "mcpx__search_tools" {
		t.Errorf("name = %q, want mcpx__search_tools", def.Name)
	}
	schema := string(def.InputSchema)
	if !strings.Contains(schema, "\"queries\"") {
		t.Error("schema lost 'queries' property")
	}
	for _, alias := range []string{"\"query\"", "\"max_results\"", "\"namespace\""} {
		if !strings.Contains(schema, alias) {
			t.Errorf("schema missing compatibility alias %s", alias)
		}
	}
	// The new optional namespaces filter should be advertised.
	if !strings.Contains(schema, "\"namespaces\"") {
		t.Error("schema missing 'namespaces' property")
	}
}

func TestDecodeSearchToolsArgs_CommonModelAliases(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		queries    []string
		limit      int
		namespaces []string
	}{
		{"canonical", `{"queries":["task create"]}`, []string{"task create"}, 0, nil},
		{"canonical string", `{"queries":"task create"}`, []string{"task create"}, 0, nil},
		{"singular string", `{"query":"task create"}`, []string{"task create"}, 0, nil},
		{"singular array", `{"query":["task create","task list"]}`, []string{"task create", "task list"}, 0, nil},
		{"q alias", `{"q":"task create"}`, []string{"task create"}, 0, nil},
		{"nested legacy wrapper", `{"query":{"queries":["task create"]}}`, []string{"task create"}, 0, nil},
		{"limit aliases", `{"query":"task","max_results":7}`, []string{"task"}, 7, nil},
		{"namespace alias", `{"query":"issues","namespace":"github"}`, []string{"issues"}, 0, []string{"github"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeSearchToolsArgs(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual([]string(got.Queries), tc.queries) ||
				got.Limit != tc.limit ||
				!reflect.DeepEqual([]string(got.Namespaces), tc.namespaces) {
				t.Fatalf("decoded = queries=%v limit=%d namespaces=%v", got.Queries, got.Limit, got.Namespaces)
			}
		})
	}
}

func TestSearchToolsDefinition_AdvertisesHardBounds(t *testing.T) {
	schema := string(searchToolsDefinition().InputSchema)
	for _, want := range []string{`"maxItems": 10`, `"minimum": 1`, `"maximum": 20`} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema missing model-visible bound %s", want)
		}
	}
}

// TestSynonymTable_KnownClusters spot-checks the embedded synonyms.txt file.
func TestSynonymTable_KnownClusters(t *testing.T) {
	syn := defaultSynonyms()

	mustCluster(t, syn, "create", "make", "add", "raise")
	mustCluster(t, syn, "send", "post", "publish")
	mustCluster(t, syn, "list", "get", "fetch", "search")

	// Unknown term returns itself only.
	exp := syn.expandTerm("definitelynotaverb")
	if len(exp) != 1 || exp[0] != "definitelynotaverb" {
		t.Errorf("unknown term should return itself, got %v", exp)
	}
}

// mustCluster asserts that the given anchor term's cluster contains all the
// listed members.
func mustCluster(t *testing.T, syn *synonymTable, anchor string, members ...string) {
	t.Helper()
	got := syn.expandTerm(anchor)
	set := make(map[string]struct{}, len(got))
	for _, g := range got {
		set[g] = struct{}{}
	}
	for _, m := range members {
		if _, ok := set[m]; !ok {
			t.Errorf("cluster for %q missing %q (got %v)", anchor, m, got)
		}
	}
}

// TestSnippet_Truncation makes sure long descriptions get clipped cleanly.
func TestSnippet_Truncation(t *testing.T) {
	long := strings.Repeat("a", 200)
	s := snippet(long)
	if len(s) > 120 {
		t.Errorf("snippet too long: %d chars", len(s))
	}
	if !strings.HasSuffix(s, "...") {
		t.Errorf("expected ellipsis suffix, got %q", s)
	}

	// Multi-line descriptions are flattened.
	multi := "first paragraph\n\nsecond paragraph"
	if got := snippet(multi); got != "first paragraph" {
		t.Errorf("multi-paragraph trim = %q, want %q", got, "first paragraph")
	}
}

// TestGroupByNamespace verifies grouping preserves intra-group score order.
func TestGroupByNamespace(t *testing.T) {
	hits := []rankedTool{
		{Tool: Tool{Name: "github__list_issues"}, Score: 0.9},
		{Tool: Tool{Name: "slack__send_message"}, Score: 0.8},
		{Tool: Tool{Name: "github__create_issue"}, Score: 0.7},
	}
	groups := groupByNamespace(hits)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if groups[0].Namespace != "github" || len(groups[0].Hits) != 2 {
		t.Errorf("github group malformed: %+v", groups[0])
	}
	if groups[0].Hits[0].Score < groups[0].Hits[1].Score {
		t.Errorf("intra-group order not preserved: %+v", groups[0].Hits)
	}
}

// inTopN returns true if name appears in the first n results.
func inTopN(ranked []rankedTool, name string, n int) bool {
	if n > len(ranked) {
		n = len(ranked)
	}
	for i := 0; i < n; i++ {
		if ranked[i].Tool.Name == name {
			return true
		}
	}
	return false
}

// topNames returns up to the first n names from a ranked slice.
func topNames(ranked []rankedTool, n int) []string {
	if n > len(ranked) {
		n = len(ranked)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = ranked[i].Tool.Name
	}
	return out
}
