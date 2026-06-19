package assist

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/models"
)

// Guidance kinds (DESIGN §4.4 inline nudges). The GUI renders each as a single
// calm in-field line (warn tone, one-click apply, never a popup).
const (
	GuidanceMissingCriteria = "missing-acceptance-criteria"
	GuidanceLinkMemory      = "link-related-memory"
	GuidanceAutoTag         = "auto-tag"
	GuidanceEntityExtract   = "entity-extraction"
)

// Nudge is one inline guidance suggestion (DESIGN §4.4). Message is the human
// single-line copy; Apply carries the structured change the GUI applies with
// one click (a tag to add, a [[ref]] to insert, a checklist to append).
type Nudge struct {
	Kind    string     `json:"kind"`
	Message string     `json:"message"`
	Apply   NudgeApply `json:"apply"`
}

// NudgeApply is the one-click change a nudge proposes. Exactly one field set:
// AddTag (auto-tag), InsertRef (link-memory / entity-extract), or AppendBody
// (missing-criteria checklist insert).
type NudgeApply struct {
	AddTag     string `json:"add_tag,omitempty"`
	InsertRef  string `json:"insert_ref,omitempty"`
	AppendBody string `json:"append_body,omitempty"`
}

// GuidanceRequest is one inline-guidance ask: the record being edited (title +
// body + current status/tags) plus the workspace for ref scoping. The status +
// tags let the deterministic rules fire WITHOUT a model (the missing-criteria +
// auto-tag nudges need no LLM); the model only enriches link-memory /
// entity-extraction when a profile is present.
type GuidanceRequest struct {
	RecordID     string   `json:"record_id,omitempty"`
	Title        string   `json:"title,omitempty"`
	Body         string   `json:"body"`
	Status       string   `json:"status,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Workspace    string   `json:"workspace,omitempty"`
	ModelProfile string   `json:"model_profile,omitempty"`
}

// acceptanceCriteriaRe detects an existing checklist / acceptance-criteria
// section so the missing-criteria nudge does not re-fire once one exists.
var acceptanceCriteriaRe = regexp.MustCompile(`(?im)^\s*(?:-\s*\[[ xX]\]|acceptance criteria|## *criteria)`)

// refMentionRe finds [[ref]] mentions in prose the entity-extraction nudge can
// offer to formalise into the composes/entities field.
var refMentionRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// Guidance returns 0..N inline nudges for req (DESIGN §4.4). Deterministic
// rules (missing-criteria, auto-tag, entity-extraction from prose [[refs]])
// run with NO model. When a model profile resolves, the link-related-memory
// nudge is added from the model. NEVER returns ErrNoProfile: guidance always
// works (the model-backed nudge is simply omitted when no profile exists), so
// the GUI gets the deterministic nudges even with no model wired. The caller
// applies the one-pulse-per-record law (guidance vs memory candidate) on the
// client; the higher-signal pulse wins.
func (a *Assistant) Guidance(ctx context.Context, req GuidanceRequest) ([]Nudge, string, error) {
	body := strings.TrimSpace(req.Body)
	out := deterministicNudges(req, body)

	// The model-backed link-related-memory nudge is best-effort: a missing
	// profile or a model error degrades to the deterministic nudges only.
	profile := ""
	if body != "" {
		cfg, name, err := a.resolveConfig(ctx, req.ModelProfile)
		if err == nil {
			profile = name
			if n, ok := a.linkMemoryNudge(ctx, cfg, req, body); ok {
				out = append(out, n)
			}
		}
	}
	return out, profile, nil
}

// deterministicNudges runs the no-model rules: a task in doing/review with no
// acceptance criteria, an auto-tag mined from the body, and [[ref]] mentions
// in prose that should be formalised into composes/entities.
func deterministicNudges(req GuidanceRequest, body string) []Nudge {
	out := make([]Nudge, 0, 3)

	// Missing acceptance criteria on a task that has left "open".
	st := strings.ToLower(strings.TrimSpace(req.Status))
	if (st == "doing" || st == "review") && body != "" && !acceptanceCriteriaRe.MatchString(body) {
		out = append(out, Nudge{
			Kind:    GuidanceMissingCriteria,
			Message: "no acceptance criteria - add a checklist?",
			Apply:   NudgeApply{AppendBody: "\n\n## Acceptance criteria\n- [ ] "},
		})
	}

	// Auto-tag: a keyword in the body that is not already a tag.
	if tag := suggestTag(body, req.Tags); tag != "" {
		out = append(out, Nudge{
			Kind:    GuidanceAutoTag,
			Message: "looks like a #" + tag + " " + recordNoun(req.Status) + " - add tag?",
			Apply:   NudgeApply{AddTag: tag},
		})
	}

	// Entity extraction: a [[ref]] in prose not yet formalised.
	if ref := firstProseRef(body); ref != "" {
		out = append(out, Nudge{
			Kind:    GuidanceEntityExtract,
			Message: "mentions [[" + ref + "]] in prose - link it?",
			Apply:   NudgeApply{InsertRef: ref},
		})
	}
	return out
}

// recordNoun returns "task" when a status is present (so the record is a task),
// else "note" — keeps the auto-tag copy honest without naming a record kind we
// can't infer.
func recordNoun(status string) string {
	if strings.TrimSpace(status) != "" {
		return "task"
	}
	return "note"
}

// tagKeywords maps a body keyword to the tag it implies. Deterministic, small,
// and honest — the model is not consulted for the cheap auto-tag path.
var tagKeywords = map[string]string{
	"scheduler":  "scheduler",
	"cron":       "scheduler",
	"telegram":   "telegram",
	"deploy":     "deploy",
	"sandbox":    "security",
	"injection":  "security",
	"migration":  "db",
	"sqlite":     "db",
	"mesh":       "mesh",
	"worker":     "workers",
	"regression": "bug",
}

// suggestTag returns the first keyword tag implied by body that is not already
// present (case-insensitive), or "".
func suggestTag(body string, existing []string) string {
	have := make(map[string]struct{}, len(existing))
	for _, t := range existing {
		have[strings.ToLower(strings.TrimSpace(t))] = struct{}{}
	}
	lower := strings.ToLower(body)
	for kw, tag := range tagKeywords {
		if _, ok := have[tag]; ok {
			continue
		}
		if strings.Contains(lower, kw) {
			return tag
		}
	}
	return ""
}

// firstProseRef returns the first [[ref]] mentioned in the body, or "".
func firstProseRef(body string) string {
	m := refMentionRe.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// linkMemoryCandidateLimit caps the shortlist of existing memories handed to
// the model. Small enough to keep the prompt cheap, wide enough that the truly
// related memory is in the set; the model picks ONLY from this list.
const linkMemoryCandidateLimit = 8

// linkMemoryNudge asks the model to pick ONE existing, indexed memory worth
// linking — grounded against the live index so the proposed [[ref]] always
// resolves (DESIGN §4.4). It returns ok=false when: no index is wired (an
// ungrounded nudge would mint a dangling ref), the index has no candidates, the
// model declines, or the model's pick is not in the candidate set (a fabricated
// slug is dropped server-side, never surfaced).
func (a *Assistant) linkMemoryNudge(ctx context.Context, cfg models.Config, req GuidanceRequest, body string) (Nudge, bool) {
	// Grounding is mandatory: without the index the nudge is suppressed so it
	// can never insert a [[ref]] that resolves to nothing.
	if a.index == nil {
		return Nudge{}, false
	}
	// Query the same FTS5 path the typeahead uses for candidates in the record's
	// workspace scope. Seed the query from the title (falling back to the body)
	// so the candidate set is relevant, not arbitrary.
	seed := strings.TrimSpace(req.Title)
	if seed == "" {
		seed = body
	}
	cands, err := a.index.SearchMemories(ctx, seed, req.Workspace, linkMemoryCandidateLimit)
	if err != nil || len(cands) == 0 {
		return Nudge{}, false
	}
	// allowed is the authoritative set the model's pick must be in; a slug not
	// here is a fabrication and is dropped.
	allowed := make(map[string]struct{}, len(cands))
	for _, c := range cands {
		if n := strings.TrimSpace(c.Name); n != "" {
			allowed[n] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return Nudge{}, false
	}

	adapter, err := a.factory(cfg)
	if err != nil {
		return Nudge{}, false
	}
	out, err := adapter.Send(ctx, models.SendRequest{
		System:    linkMemorySystemPrompt,
		Messages:  []models.Message{{Role: models.RoleUser, Content: guidanceUserPrompt(req, body, cands)}},
		MaxTokens: 128,
	})
	if err != nil {
		return Nudge{}, false
	}
	name := parseLinkMemory(out.Text)
	if name == "" {
		return Nudge{}, false
	}
	// Server-side validation: drop any slug the model invented that is not an
	// indexed memory, so the one-click apply never inserts a dangling ref.
	if _, ok := allowed[name]; !ok {
		return Nudge{}, false
	}
	return Nudge{
		Kind:    GuidanceLinkMemory,
		Message: "related: [[" + name + "]] - link it?",
		Apply:   NudgeApply{InsertRef: name},
	}, true
}

// linkMemorySystemPrompt instructs the model to pick at most ONE memory FROM
// THE PROVIDED LIST (it is never asked to invent a slug). Returning {} when
// nothing in the list is clearly related is the high bar.
const linkMemorySystemPrompt = `You pick at most ONE existing memory worth linking from a note the user is writing.
You are given a numbered list of EXISTING memories (each a "name" slug + title).
Return ONLY a JSON object: {"name": "<name from the list>"} or {} if none is clearly related.
The name MUST be copied verbatim from the provided list. NEVER invent a name that is not in the list.`

// guidanceUserPrompt frames the record + the grounded candidate list for the
// link-memory ask. The candidates are the ONLY names the model may pick from.
func guidanceUserPrompt(req GuidanceRequest, body string, cands []MemoryCandidate) string {
	var b strings.Builder
	if req.Title != "" {
		b.WriteString("Title: ")
		b.WriteString(req.Title)
		b.WriteString("\n\n")
	}
	b.WriteString("Body:\n")
	b.WriteString(body)
	b.WriteString("\n\nExisting memories you may link (pick a name from THIS list only):\n")
	for i, c := range cands {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". name=")
		b.WriteString(c.Name)
		if t := strings.TrimSpace(c.Title); t != "" {
			b.WriteString("  title=")
			b.WriteString(t)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// parseLinkMemory pulls the memory slug out of the model's JSON object,
// tolerating surrounding prose / code fences.
func parseLinkMemory(raw string) string {
	s := strings.TrimSpace(raw)
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	var parsed struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Name)
}
