package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

type contextCostFixture struct {
	handler     *handler
	db          *sqlite.DB
	workspaceID string
	workspace   string
	sessionID   string
	memoryID    string
	taskID      string
	meshID      string
}

type contextCostMetric struct {
	Surface             string `json:"surface"`
	Bytes               int    `json:"bytes"`
	ApproxTokens        int    `json:"approx_tokens"`
	RecommendedMaxBytes int    `json:"recommended_max_bytes,omitempty"`
	ToolCount           int    `json:"tool_count,omitempty"`
	ContentBlocks       int    `json:"content_blocks,omitempty"`
	TextBytes           int    `json:"text_bytes,omitempty"`
	ItemCount           int    `json:"item_count,omitempty"`
	Fixture             string `json:"fixture,omitempty"`
	Note                string `json:"note,omitempty"`
}

func TestContextCostBaseline(t *testing.T) {
	if os.Getenv("MCPLEXER_CONTEXT_COST_BASELINE") != "1" {
		t.Skip("set MCPLEXER_CONTEXT_COST_BASELINE=1 to emit context-cost metrics")
	}

	ctx := context.Background()
	f := newContextCostFixture(t, ctx)

	var metrics []contextCostMetric
	addRawContextCostMetric(t, &metrics, "tools_list_static_default_slim",
		mustToolsList(t, ctx, f.handler), contextCostRecommendedMax["tools_list_static_default_slim"],
		"default SlimSurface=true, SlimTools=true")

	saveContextCostSettings(t, ctx, f.handler, func(s config.Settings) config.Settings {
		s.SlimSurface = false
		s.SlimTools = true
		return s
	})
	addRawContextCostMetric(t, &metrics, "tools_list_static_full_surface_slim_schemas",
		mustToolsList(t, ctx, f.handler), contextCostRecommendedMax["tools_list_static_full_surface_slim_schemas"],
		"SlimSurface=false, SlimTools=true")

	saveContextCostSettings(t, ctx, f.handler, func(s config.Settings) config.Settings {
		s.SlimSurface = false
		s.SlimTools = false
		return s
	})
	addRawContextCostMetric(t, &metrics, "tools_list_static_full_surface_full_schemas",
		mustToolsList(t, ctx, f.handler), contextCostRecommendedMax["tools_list_static_full_surface_full_schemas"],
		"SlimSurface=false, SlimTools=false")

	saveContextCostSettings(t, ctx, f.handler, func(s config.Settings) config.Settings {
		s.SlimSurface = true
		s.SlimTools = true
		return s
	})

	searchQueries := []string{
		"receive mesh messages and inspect pending coordination",
		"list tasks get task details",
		"recall memory get memory",
		"search skills get skill body",
	}
	searchSummary, rpcErr := f.handler.handleDiscoverTools(ctx, searchQueries, "summary", "", nil, 0)
	if rpcErr != nil {
		t.Fatalf("search summary: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "search_tools_summary_multi_query", searchSummary,
		contextCostRecommendedMax["search_tools_summary_multi_query"], len(searchQueries),
		"four representative workflow queries")

	searchFull, rpcErr := f.handler.handleDiscoverTools(ctx, searchQueries, "full", "", nil, 0)
	if rpcErr != nil {
		t.Fatalf("search full: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "search_tools_full_multi_query", searchFull,
		contextCostRecommendedMax["search_tools_full_multi_query"], len(searchQueries),
		"same queries, detail=full")

	exactFull, rpcErr := f.handler.handleDiscoverTools(ctx, nil, "", "task__get", nil, 0)
	if rpcErr != nil {
		t.Fatalf("search exact tool: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "search_tools_full_exact_task_get", exactFull,
		contextCostRecommendedMax["search_tools_full_exact_task_get"], 1,
		"single exact tool signature")

	execSmall, rpcErr := f.handler.handleCodeExecute(ctx, `print("context cost baseline: ok")`)
	if rpcErr != nil {
		t.Fatalf("execute small print: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "execute_code_result_small_print", execSmall,
		contextCostRecommendedMax["execute_code_result_small_print"], 1,
		"single print() line")

	limit := f.handler.codeModeMaxOutputBytes(ctx)
	largePayload := strings.Repeat("p", limit+4096)
	quoted, err := json.Marshal(largePayload)
	if err != nil {
		t.Fatalf("quote large payload: %v", err)
	}
	execLarge, rpcErr := f.handler.handleCodeExecute(ctx, "print("+string(quoted)+")")
	if rpcErr != nil {
		t.Fatalf("execute large print: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "execute_code_result_large_print_truncated", execLarge,
		contextCostRecommendedMax["execute_code_result_large_print_truncated"], 1,
		fmt.Sprintf("configured code_mode_max_output_bytes=%d", limit))

	meshReceive, rpcErr := f.handler.handleMeshReceive(ctx,
		json.RawMessage(`{"filter":"all","max_results":5,"name":"baseline-auditor","role":"measurement"}`))
	if rpcErr != nil {
		t.Fatalf("mesh receive: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "mesh_receive_one_6kb_body_default_preview", meshReceive,
		contextCostRecommendedMax["mesh_receive_one_6kb_body_default_preview"], 1,
		"one targeted 6000 byte message, default receive preview")

	saveContextCostSettings(t, ctx, f.handler, func(s config.Settings) config.Settings {
		s.MeshReceivePreviewBytes = mesh.MaxReceivePreviewBytes
		return s
	})
	meshReceiveMax, rpcErr := f.handler.handleMeshReceive(ctx,
		json.RawMessage(`{"filter":"all","max_results":5,"max_content_bytes":2048,"name":"baseline-auditor","role":"measurement"}`))
	if rpcErr != nil {
		t.Fatalf("mesh receive max preview: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "mesh_receive_one_6kb_body_max_preview", meshReceiveMax,
		contextCostRecommendedMax["mesh_receive_one_6kb_body_max_preview"], 1,
		"same message, configured max receive preview")
	saveContextCostSettings(t, ctx, f.handler, func(s config.Settings) config.Settings {
		s.MeshReceivePreviewBytes = config.DefaultSettings().MeshReceivePreviewBytes
		return s
	})

	meshHydrate, rpcErr := f.handler.handleMeshHydrate(ctx,
		json.RawMessage(fmt.Sprintf(`{"message_id":%q}`, f.meshID)))
	if rpcErr != nil {
		t.Fatalf("mesh hydrate: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "mesh_hydrate_one_6kb_body_default", meshHydrate,
		contextCostRecommendedMax["mesh_hydrate_one_6kb_body_default"], 1,
		"same message, explicit hydrate default content budget")

	taskList, rpcErr := f.handler.handleTaskList(ctx, json.RawMessage(`{"limit":5}`))
	if rpcErr != nil {
		t.Fatalf("task list: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "task_list_five", taskList,
		contextCostRecommendedMax["task_list_five"], 5,
		"five tasks with description/meta/status history")

	taskGet, rpcErr := f.handler.handleTaskGet(ctx,
		json.RawMessage(fmt.Sprintf(`{"id":%q}`, f.taskID)))
	if rpcErr != nil {
		t.Fatalf("task get: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "task_get_single_with_notes", taskGet,
		contextCostRecommendedMax["task_get_single_with_notes"], 1,
		"single task with two notes")

	memoryRecall, rpcErr := f.handler.handleMemoryRecall(ctx,
		json.RawMessage(`{"query":"context cost memory recall","limit":5}`))
	if rpcErr != nil {
		t.Fatalf("memory recall: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "memory_recall_five", memoryRecall,
		contextCostRecommendedMax["memory_recall_five"], 5,
		"five workspace memories, recall preview capped by handler")

	memoryGet, rpcErr := f.handler.handleMemoryGet(ctx,
		json.RawMessage(fmt.Sprintf(`{"id":%q}`, f.memoryID)))
	if rpcErr != nil {
		t.Fatalf("memory get: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "memory_get_single_full_body", memoryGet,
		contextCostRecommendedMax["memory_get_single_full_body"], 1,
		"single memory with full 2300 byte body")

	skillSearch, rpcErr := f.handler.handleSkillSearch(ctx,
		json.RawMessage(`{"query":"measure context cost payload audit","limit":3}`))
	if rpcErr != nil {
		t.Fatalf("skill search: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "skill_search_three", skillSearch,
		contextCostRecommendedMax["skill_search_three"], 3,
		"three published skills")

	skillGet, rpcErr := f.handler.handleSkillGet(ctx,
		json.RawMessage(`{"name":"context-cost-audit"}`))
	if rpcErr != nil {
		t.Fatalf("skill get: %v", rpcErr)
	}
	addToolContextCostMetric(&metrics, "skill_get_single_body", skillGet,
		contextCostRecommendedMax["skill_get_single_body"], 1,
		"one SKILL.md body, include_bundle=false")

	if os.Getenv("MCPLEXER_CONTEXT_COST_ENFORCE") == "1" {
		for _, metric := range metrics {
			if metric.RecommendedMaxBytes > 0 && metric.Bytes > metric.RecommendedMaxBytes {
				t.Errorf("%s bytes=%d exceeds recommended max %d",
					metric.Surface, metric.Bytes, metric.RecommendedMaxBytes)
			}
		}
	}

	out, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		t.Fatalf("marshal metrics: %v", err)
	}
	t.Logf("CONTEXT_COST_BASELINE=%s", out)
}

var contextCostRecommendedMax = map[string]int{
	"tools_list_static_default_slim":              20000,
	"tools_list_static_full_surface_slim_schemas": 120000,
	"tools_list_static_full_surface_full_schemas": 180000,
	"search_tools_summary_multi_query":            16000,
	"search_tools_full_multi_query":               60000,
	"search_tools_full_exact_task_get":            12000,
	"execute_code_result_small_print":             2000,
	"execute_code_result_large_print_truncated":   32000,
	"mesh_receive_one_6kb_body_default_preview":   2000,
	"mesh_receive_one_6kb_body_max_preview":       4000,
	"mesh_hydrate_one_6kb_body_default":           9000,
	"task_list_five":                              18000,
	"task_get_single_with_notes":                  12000,
	"memory_recall_five":                          5000,
	"memory_get_single_full_body":                 4000,
	"skill_search_three":                          3000,
	"skill_get_single_body":                       8000,
}

func newContextCostFixture(t *testing.T, ctx context.Context) contextCostFixture {
	t.Helper()

	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	db, err := sqlite.New(ctx, filepath.Join(root, "context-cost.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{
		ID:       "ws-context-cost",
		Name:     "context-cost",
		RootPath: workspaceRoot,
		Tags:     json.RawMessage(`["measurement"]`),
	}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	sessionID := "sess-context-cost"
	sess := &store.Session{ID: sessionID, ClientType: "codex-test"}
	if err := db.CreateSession(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	servers, toolMap := contextCostDownstreamCatalog()
	for i := range servers {
		if err := db.CreateDownstreamServer(ctx, &servers[i]); err != nil {
			t.Fatalf("create downstream %s: %v", servers[i].ID, err)
		}
	}

	settingsSvc := config.NewSettingsService(db)
	settings := config.DefaultSettings()
	settings.CodeModeTimeoutSec = 5
	settings.CodeModeMaxOutputBytes = 24 * 1024
	if err := settingsSvc.Save(ctx, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	meshMgr := mesh.NewManager(db)
	memorySvc := memory.NewService(db, memory.NoopEmbedder{}, nil)
	taskSvc := tasks.New(db)
	taskSvc.SetWorkspaceLookup(db)
	skillReg := skillregistry.New(db)
	approvals := approval.NewManager(db, approval.NewBus())
	secretPrompts, err := ephemeral.New(ctx, db, root, nil, nil, nil)
	if err != nil {
		t.Fatalf("secret prompt manager: %v", err)
	}
	t.Cleanup(secretPrompts.Stop)
	enc, err := secrets.NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("secret encryptor: %v", err)
	}

	lister := &mockToolLister{tools: toolMap}
	h := newHandler(
		db,
		routing.NewEngine(db),
		lister,
		nil,
		TransportInternal,
		approvals,
		meshMgr,
		nil,
		settingsSvc,
		nil,
		nil,
		nil,
		secretPrompts,
		secrets.NewManager(db, enc),
		nil,
		skillReg,
		nil,
		memorySvc,
		nil,
		nil,
	)
	h.tasksSvc = taskSvc
	h.sessions.session = sess
	h.sessions.clientPath = workspaceRoot
	h.sessions.wsChain = []routing.WorkspaceAncestor{{
		ID:       ws.ID,
		Name:     ws.Name,
		RootPath: ws.RootPath,
	}}

	f := contextCostFixture{
		handler:     h,
		db:          db,
		workspaceID: ws.ID,
		workspace:   workspaceRoot,
		sessionID:   sessionID,
	}
	f.seed(t, ctx)
	return f
}

func (f *contextCostFixture) seed(t *testing.T, ctx context.Context) {
	t.Helper()
	f.seedTasks(t, ctx)
	f.seedMemory(t, ctx)
	f.seedMesh(t, ctx)
	f.seedSkills(t, ctx)
}

func (f *contextCostFixture) seedTasks(t *testing.T, ctx context.Context) {
	t.Helper()
	statuses := []store.TaskStatusVocab{
		{WorkspaceID: f.workspaceID, StatusText: "open", Kind: "open", DisplayOrder: 1},
		{WorkspaceID: f.workspaceID, StatusText: "doing", Kind: "working", DisplayOrder: 2},
		{WorkspaceID: f.workspaceID, StatusText: "blocked", Kind: "blocked", DisplayOrder: 3},
		{WorkspaceID: f.workspaceID, StatusText: "review", Kind: "working", DisplayOrder: 4},
		{WorkspaceID: f.workspaceID, StatusText: "done", Kind: "done", IsTerminal: true, DisplayOrder: 5},
	}
	for i := range statuses {
		if err := f.db.UpsertTaskStatusVocab(ctx, &statuses[i]); err != nil {
			t.Fatalf("status vocab: %v", err)
		}
	}
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		history, _ := json.Marshal([]store.TaskStatusHistoryEntry{
			{At: now.Add(-time.Duration(6-i) * time.Hour), BySession: f.sessionID, Evt: "created", To: "open"},
			{At: now.Add(-time.Duration(5-i) * time.Hour), BySession: f.sessionID, Evt: "status_changed", From: "open", To: []string{"open", "doing", "blocked", "review", "done"}[i]},
		})
		meta := fmt.Sprintf("phase: measurement\nowner: audit-agent\nrisk: context-cost\nslot: %d\n", i+1)
		task := &store.Task{
			WorkspaceID:        f.workspaceID,
			Title:              fmt.Sprintf("Context cost fixture task %d", i+1),
			Description:        strings.Repeat(fmt.Sprintf("Task %d investigates payload surfaces for tools list search execute mesh task memory skill. ", i+1), 8),
			Status:             []string{"open", "doing", "blocked", "review", "done"}[i],
			Priority:           []string{"normal", "high", "normal", "low", "normal"}[i],
			TagsJSON:           json.RawMessage(`["context-cost","measurement","audit"]`),
			Meta:               meta,
			SourceSessionID:    f.sessionID,
			CreatedBySessionID: f.sessionID,
			UpdatedBySessionID: f.sessionID,
			StatusHistoryJSON:  history,
			CreatedAt:          now.Add(-time.Duration(6-i) * time.Hour),
			UpdatedAt:          now.Add(-time.Duration(5-i) * time.Hour),
		}
		if err := f.db.CreateTask(ctx, task); err != nil {
			t.Fatalf("create task %d: %v", i+1, err)
		}
		if i == 0 {
			f.taskID = task.ID
			for j := 0; j < 2; j++ {
				note := &store.TaskNote{
					TaskID:          task.ID,
					AuthorSessionID: f.sessionID,
					AuthorKind:      store.TaskSourceAgent,
					Body:            strings.Repeat(fmt.Sprintf("Task note %d records context-cost observations and measured payload deltas. ", j+1), 5),
					CreatedAt:       now.Add(-time.Duration(j+1) * time.Hour),
				}
				if err := f.db.AppendTaskNote(ctx, note); err != nil {
					t.Fatalf("append task note: %v", err)
				}
			}
		}
	}
}

func (f *contextCostFixture) seedMemory(t *testing.T, ctx context.Context) {
	t.Helper()
	for i := 0; i < 5; i++ {
		body := strings.Repeat(fmt.Sprintf("Memory %d captures context cost recall payload sizing for mesh task memory skill search tools. ", i+1), 8)
		if i == 0 {
			body = strings.Repeat("Long memory body for context cost memory get full body measurement. ", 38)
		}
		id, err := f.handler.memorySvc.Write(ctx, memory.WriteOptions{
			Name:        fmt.Sprintf("context-cost-memory-%d", i+1),
			Kind:        store.MemoryKindNote,
			Content:     body,
			Tags:        []string{"context-cost", "measurement", "audit"},
			WorkspaceID: &f.workspaceID,
			SourceKind:  store.MemorySourceAgent,
		})
		if err != nil {
			t.Fatalf("write memory %d: %v", i+1, err)
		}
		if i == 0 {
			f.memoryID = id
		}
	}
}

func (f *contextCostFixture) seedMesh(t *testing.T, ctx context.Context) {
	t.Helper()
	receiver := mesh.SessionMeta{
		SessionID:     f.sessionID,
		WorkspaceIDs:  []string{f.workspaceID},
		ClientType:    "codex-test",
		WorkspacePath: f.workspace,
	}
	if _, err := f.handler.mesh.Receive(ctx, receiver, mesh.ReceiveRequest{
		Name: "baseline-auditor",
		Role: "measurement",
	}); err != nil {
		t.Fatalf("register mesh receiver: %v", err)
	}
	sender := mesh.SessionMeta{
		SessionID:     "sess-context-cost-sender",
		WorkspaceIDs:  []string{f.workspaceID},
		ClientType:    "fixture",
		WorkspacePath: f.workspace,
	}
	if _, err := f.handler.mesh.Receive(ctx, sender, mesh.ReceiveRequest{
		Name: "fixture-sender",
		Role: "producer",
	}); err != nil {
		t.Fatalf("register mesh sender: %v", err)
	}
	body := strings.Repeat("mesh context-cost body segment for receive-size measurement ", 104)
	if len(body) < 6000 {
		body += strings.Repeat("x", 6000-len(body))
	}
	body = body[:6000]
	msg, err := f.handler.mesh.Send(ctx, sender, mesh.SendRequest{
		Kind:     "finding",
		Priority: "high",
		Content:  body,
		Audience: f.sessionID,
	})
	if err != nil {
		t.Fatalf("send mesh fixture: %v", err)
	}
	f.meshID = msg.ID
}

func (f *contextCostFixture) seedSkills(t *testing.T, ctx context.Context) {
	t.Helper()
	skills := []string{"context-cost-audit", "mesh-receive-audit", "task-memory-audit"}
	for i, name := range skills {
		body := contextCostSkillBody(name, i+1)
		if _, err := f.handler.skillRegistry.Publish(ctx, skillregistry.PublishOptions{
			Name:             name,
			Body:             body,
			Author:           "context-cost-fixture",
			CreatedByAgentID: f.sessionID,
		}); err != nil {
			t.Fatalf("publish skill %s: %v", name, err)
		}
	}
}

func contextCostSkillBody(name string, n int) string {
	return fmt.Sprintf(`---
name: %s
description: Use when measuring context cost payload sizes for MCPlexer audit surfaces, including tools list, search tools, execute code, mesh receive, task, memory, and skill registry responses.
category: measurement
tags:
  - context-cost
  - audit
---

# Context Cost Skill %d

Use this skill to inspect MCP response sizes, compare payload boundaries, and keep measurements reproducible.

## Workflow

1. Inspect static tool inventory size.
2. Search for full and summary discovery payloads.
3. Measure result envelopes for task, memory, mesh, and skill calls.
4. Record thresholds that leave room for schema drift but catch accidental context expansion.

## Notes

%s
`, name, n, strings.Repeat("The baseline should be deterministic and should never read private user state. ", 28))
}

func contextCostDownstreamCatalog() ([]store.DownstreamServer, map[string]json.RawMessage) {
	spec := map[string][]Tool{
		"github": {
			contextCostCatalogTool("list_issues", "List GitHub issues with labels, assignees, state, and repository filters."),
			contextCostCatalogTool("create_issue", "Create a GitHub issue with title, markdown body, labels, and assignee handles."),
			contextCostCatalogTool("get_pull_request", "Fetch a pull request summary with review status and changed file counts."),
			contextCostCatalogTool("search_code", "Search repository code by query and path filters."),
			contextCostCatalogTool("list_comments", "List pull request review comments and unresolved discussion threads."),
			contextCostCatalogTool("create_comment", "Create a review or issue comment."),
		},
		"linear": {
			contextCostCatalogTool("list_tasks", "List Linear tasks and issues by project, state, assignee, and label."),
			contextCostCatalogTool("create_issue", "Create a Linear issue with project, team, priority, and description."),
			contextCostCatalogTool("update_issue", "Update Linear issue fields including status, assignee, and priority."),
			contextCostCatalogTool("search_issues", "Search Linear issues by natural language query."),
			contextCostCatalogTool("list_projects", "List Linear projects and milestones."),
			contextCostCatalogTool("add_comment", "Add a comment to a Linear issue."),
		},
		"gmail": {
			contextCostCatalogTool("search_messages", "Search Gmail messages by sender, subject, labels, and date query."),
			contextCostCatalogTool("get_thread", "Fetch a Gmail thread with message snippets and headers."),
			contextCostCatalogTool("draft_reply", "Draft a Gmail reply without sending it."),
			contextCostCatalogTool("send_message", "Send a Gmail message to recipients with subject and body."),
			contextCostCatalogTool("list_labels", "List Gmail labels."),
			contextCostCatalogTool("archive_thread", "Archive a Gmail thread."),
		},
		"postgres": {
			contextCostCatalogTool("query", "Run a SQL query after schema inspection."),
			contextCostCatalogTool("list_schemas", "List database schemas."),
			contextCostCatalogTool("list_tables", "List tables in a schema."),
			contextCostCatalogTool("describe_table", "Describe columns and indexes for a table."),
			contextCostCatalogTool("explain_query", "Run EXPLAIN for a SQL query."),
			contextCostCatalogTool("list_connections", "List active database connections."),
		},
		"wordpress": {
			contextCostCatalogTool("search_posts", "Search WordPress posts and pages."),
			contextCostCatalogTool("get_post", "Fetch a WordPress post body and metadata."),
			contextCostCatalogTool("update_post", "Update a WordPress post title, body, or status."),
			contextCostCatalogTool("list_media", "List WordPress media assets."),
			contextCostCatalogTool("create_draft", "Create a WordPress draft."),
			contextCostCatalogTool("publish_post", "Publish a WordPress post."),
		},
		"customer": {
			contextCostCatalogTool("search_accounts", "Search customer accounts by name, email, or domain."),
			contextCostCatalogTool("get_customer_snapshot", "Fetch a customer snapshot with plan, billing, and support health."),
			contextCostCatalogTool("list_invoices", "List invoices for a customer."),
			contextCostCatalogTool("create_ticket", "Create a customer support ticket."),
			contextCostCatalogTool("update_plan", "Update a customer's subscription plan."),
			contextCostCatalogTool("record_note", "Record a customer account note."),
		},
	}

	servers := make([]store.DownstreamServer, 0, len(spec))
	toolMap := make(map[string]json.RawMessage, len(spec))
	for namespace, tools := range spec {
		id := namespace + "-srv"
		servers = append(servers, store.DownstreamServer{
			ID:            id,
			Name:          namespace,
			Transport:     "stdio",
			Command:       "fixture",
			ToolNamespace: namespace,
			Discovery:     "static",
		})
		toolMap[id] = toolsJSON(tools...)
	}
	return servers, toolMap
}

func contextCostCatalogTool(name, desc string) Tool {
	return Tool{
		Name:        name,
		Description: desc + " Fixture schema includes common filters to approximate real MCP tool metadata.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {"type": "string", "description": "Search or filter query."},
				"limit": {"type": "integer", "description": "Maximum number of results."},
				"id": {"type": "string", "description": "Resource identifier."},
				"metadata": {
					"type": "object",
					"description": "Optional structured metadata.",
					"properties": {
						"source": {"type": "string", "description": "Caller-provided source label."},
						"tags": {"type": "array", "items": {"type": "string"}, "description": "Caller tags."}
					}
				}
			}
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           name,
			ReadOnlyHint:    boolPtr(true),
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(true),
		}),
	}
}

func mustToolsList(t *testing.T, ctx context.Context, h *handler) json.RawMessage {
	t.Helper()
	raw, rpcErr := h.handleToolsList(ctx)
	if rpcErr != nil {
		t.Fatalf("tools/list: %v", rpcErr)
	}
	return raw
}

func saveContextCostSettings(
	t *testing.T,
	ctx context.Context,
	h *handler,
	edit func(config.Settings) config.Settings,
) {
	t.Helper()
	settings := h.settingsSvc.Load(ctx)
	settings = edit(settings)
	if err := h.settingsSvc.Save(ctx, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
}

func addRawContextCostMetric(
	t *testing.T,
	metrics *[]contextCostMetric,
	surface string,
	raw json.RawMessage,
	recommendedMax int,
	note string,
) {
	t.Helper()
	var parsed struct {
		Tools []Tool `json:"tools"`
	}
	_ = json.Unmarshal(raw, &parsed)
	bytes := len(raw)
	*metrics = append(*metrics, contextCostMetric{
		Surface:             surface,
		Bytes:               bytes,
		ApproxTokens:        contextCostApproxTokens(bytes),
		RecommendedMaxBytes: recommendedMax,
		ToolCount:           len(parsed.Tools),
		Note:                note,
	})
}

func addToolContextCostMetric(
	metrics *[]contextCostMetric,
	surface string,
	raw json.RawMessage,
	recommendedMax int,
	itemCount int,
	note string,
) {
	blocks, textBytes := contextCostToolResultTextStats(raw)
	bytes := len(raw)
	*metrics = append(*metrics, contextCostMetric{
		Surface:             surface,
		Bytes:               bytes,
		ApproxTokens:        contextCostApproxTokens(bytes),
		RecommendedMaxBytes: recommendedMax,
		ContentBlocks:       blocks,
		TextBytes:           textBytes,
		ItemCount:           itemCount,
		Note:                note,
	})
}

func contextCostToolResultTextStats(raw json.RawMessage) (int, int) {
	var result CallToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, 0
	}
	total := 0
	for _, c := range result.Content {
		total += len(c.Text)
	}
	return len(result.Content), total
}

func contextCostApproxTokens(bytes int) int {
	return (bytes + 3) / 4
}
