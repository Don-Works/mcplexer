package assist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/don-works/mcplexer/internal/models"
)

// Signal classifies WHY a candidate crossed the bar (DESIGN §3.5 "why?"
// reveal). decision-with-rationale is the high-bar keeper; title-restatement
// is the low-signal reject the GUI never surfaces.
const (
	SignalDecisionWithRationale = "decision-with-rationale"
	SignalTitleRestatement      = "title-restatement"
)

// Candidate is one proactive-memory suggestion (DESIGN §3.5). ContentHash
// is the server-side dedup + suppression key (stable for identical text).
type Candidate struct {
	Text        string   `json:"text"`
	Kind        string   `json:"kind"` // note|fact
	Tags        []string `json:"tags,omitempty"`
	Refs        []string `json:"refs,omitempty"`
	Signal      string   `json:"signal"`
	ContentHash string   `json:"content_hash"`
}

// MemoryCandidateRequest is one proactive-memory ask: the record the user is
// editing (title + body), the workspace for ref scoping, and the optional
// model profile.
type MemoryCandidateRequest struct {
	RecordID     string `json:"record_id,omitempty"`
	Title        string `json:"title,omitempty"`
	Body         string `json:"body"`
	Workspace    string `json:"workspace,omitempty"`
	ModelProfile string `json:"model_profile,omitempty"`
}

// MemoryCandidates returns 0..N candidates worth persisting as memories,
// deduped by content-hash, each signal-classified. Returns ErrNoProfile when
// no model resolves (caller -> 204). The high-bar threshold + the
// title-restatement reject are enforced here (server-side), not in the GUI,
// so the pulse stays meaningful: a title-restatement candidate is dropped.
func (a *Assistant) MemoryCandidates(ctx context.Context, req MemoryCandidateRequest) ([]Candidate, string, error) {
	if strings.TrimSpace(req.Body) == "" {
		return nil, "", nil
	}
	cfg, profileName, err := a.resolveConfig(ctx, req.ModelProfile)
	if err != nil {
		return nil, "", err
	}
	adapter, err := a.factory(cfg)
	if err != nil {
		// Resolved-but-undrivable profile (gated CLI, missing key/endpoint) ->
		// silent degrade (caller 204s), not a 502 on every edit (DESIGN §3.4).
		if isUnusableProfileErr(err) {
			return nil, "", ErrNoProfile
		}
		return nil, "", err
	}
	out, err := adapter.Send(ctx, models.SendRequest{
		System:    memoryCandidateSystemPrompt,
		Messages:  []models.Message{{Role: models.RoleUser, Content: memoryCandidateUserPrompt(req)}},
		MaxTokens: 512,
	})
	if err != nil {
		return nil, profileName, err
	}
	cands := parseCandidates(out.Text)
	return finalizeCandidates(cands), profileName, nil
}

// memoryCandidateSystemPrompt instructs the model to emit a strict JSON array
// of high-bar candidates. The bar is explicit: a candidate must be a decision
// WITH its rationale (or a durable fact), never a restatement of the title.
const memoryCandidateSystemPrompt = `You extract durable memories an AI agent should recall later, from a note the user is writing.
Return ONLY a JSON array (possibly empty). Each element:
  {"text": "...", "kind": "note|fact", "tags": ["..."], "signal": "decision-with-rationale|title-restatement"}
Rules:
- A candidate MUST be a decision with its rationale (imperative + a "because/so that" justification) OR a durable fact, not a restatement of the title.
- If the note only restates its own title or contains no durable knowledge, return [].
- Mark "signal" honestly. At most 3 candidates. No prose outside the JSON array.`

// memoryCandidateUserPrompt frames the record for extraction.
func memoryCandidateUserPrompt(req MemoryCandidateRequest) string {
	var b strings.Builder
	if req.Title != "" {
		b.WriteString("Title: ")
		b.WriteString(req.Title)
		b.WriteString("\n\n")
	}
	b.WriteString("Body:\n")
	b.WriteString(req.Body)
	return b.String()
}

// parseCandidates extracts the JSON array from a model reply, tolerating
// surrounding prose / code fences a non-strict model may add.
func parseCandidates(raw string) []Candidate {
	s := strings.TrimSpace(raw)
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start < 0 || end <= start {
		return nil
	}
	var parsed []Candidate
	if err := json.Unmarshal([]byte(s[start:end+1]), &parsed); err != nil {
		return nil
	}
	return parsed
}

// finalizeCandidates applies the server-side gate: drop title-restatements
// and empties, normalise kind, stamp the content-hash, and dedup by hash.
func finalizeCandidates(in []Candidate) []Candidate {
	seen := make(map[string]struct{}, len(in))
	out := make([]Candidate, 0, len(in))
	for _, c := range in {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		signal := c.Signal
		if signal != SignalDecisionWithRationale {
			// The only signal the GUI surfaces is decision-with-rationale;
			// anything else (title-restatement, blank) is dropped server-side
			// so the pulse stays high-bar.
			continue
		}
		hash := contentHash(text)
		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}
		kind := strings.TrimSpace(c.Kind)
		if kind != "fact" {
			kind = "note"
		}
		out = append(out, Candidate{
			Text:        text,
			Kind:        kind,
			Tags:        c.Tags,
			Refs:        c.Refs,
			Signal:      SignalDecisionWithRationale,
			ContentHash: hash,
		})
	}
	return out
}

// contentHash is the stable dedup + suppression key for a candidate text.
func contentHash(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:16])
}
