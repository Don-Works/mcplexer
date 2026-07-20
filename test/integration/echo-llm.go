// echo-llm is a tiny stub that speaks just enough of the OpenAI chat
// completions surface to make a worker run land status=succeeded. Used by
// the mcplexer multi-node integration harness; not for production.
//
// Two response modes:
//
//   - default ("/v1/chat/completions") — returns the constant "ok" so the
//     adapter sees a clean end-turn. Enough to verify the worker pipeline
//     fires (audit + mesh + run row), not enough to grade semantic quality.
//
//   - consolidate-mode — returns a deterministic merged digest constructed
//     from the input messages: "MERGED: <m1.content> | <m2.content> | …"
//     so the bulletproof harness D7.4 stress grader can compute a
//     reduction percentage and a recall@5 score without a real LLM key.
//     Activated either by:
//     a. POSTing to "/v1/chat/completions/consolidate" (path suffix), OR
//     b. setting the request header "X-Mcplexer-Echo-Mode: consolidate"
//     The two activation paths are equivalent — agents that can't set a
//     header (some OpenAI adapter implementations) can still pick the
//     mode via the path suffix; CI scripts that prefer header-driven
//     routing use the header.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// chatCompletionResp is the slice of the OpenAI response shape the mcplexer
// openai_compat adapter actually consumes (model name, finish_reason,
// message.content, usage.*). Everything else is decorative and the adapter
// ignores it.
type chatCompletionResp struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// chatRequest is the trimmed input shape — only the fields echo-llm
// needs to compute its response. The OpenAI request envelope carries
// dozens of optional fields; we ignore everything but model + messages.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// newCompletion builds an OpenAI-shaped response with the given
// assistant content. Token counts are decorative — the adapter uses
// them for cost tracking but cost is zero on the echo path.
func newCompletion(model, content string) chatCompletionResp {
	var r chatCompletionResp
	r.ID = "chatcmpl-echo"
	r.Object = "chat.completion"
	r.Created = time.Now().Unix()
	r.Model = model
	r.Choices = append(r.Choices, struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}{
		Index: 0,
		Message: struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{Role: "assistant", Content: content},
		FinishReason: "stop",
	})
	// Token counts are illustrative — set them proportional to content
	// length so D7.4's reduction grader sees a real signal rather than
	// hardcoded 5-token responses across every request.
	r.Usage.PromptTokens = len(content) / 4
	if r.Usage.PromptTokens == 0 {
		r.Usage.PromptTokens = 4
	}
	r.Usage.CompletionTokens = r.Usage.PromptTokens
	r.Usage.TotalTokens = r.Usage.PromptTokens + r.Usage.CompletionTokens
	return r
}

// isConsolidateMode reports whether the request should produce a
// merged-digest response rather than the default "ok". Two equivalent
// activation paths:
//  1. URL path suffix "/consolidate" on /v1/chat/completions
//  2. X-Mcplexer-Echo-Mode: consolidate request header (case-insensitive)
//
// Header check runs first because path-routing in net/http is more
// expensive than a single header read.
func isConsolidateMode(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("X-Mcplexer-Echo-Mode"), "consolidate") {
		return true
	}
	return strings.HasSuffix(r.URL.Path, "/consolidate")
}

// consolidateDigest builds the deterministic merged digest the
// bulletproof D7.4 grader asserts on. Concatenates every user-role
// message body (skipping system + assistant), prefixes with
// "MERGED: ", and joins individual sources with " | " so the grader
// can split on the separator to recover original count without
// re-parsing JSON.
//
// Determinism is load-bearing: graders cache expected outputs against
// a fixed input and a drift in the digest format invalidates the
// cache, masking real reductions. Don't reformat without bumping the
// harness's expected-output fixture.
func consolidateDigest(msgs []chatMessage) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		c := strings.TrimSpace(m.Content)
		if c == "" {
			continue
		}
		parts = append(parts, c)
	}
	if len(parts) == 0 {
		// No user input → emit the canonical empty digest so the
		// adapter still sees a valid (non-error) completion. The grader
		// treats "MERGED: " (empty body) as a no-op pass.
		return "MERGED: "
	}
	return "MERGED: " + strings.Join(parts, " | ")
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body chatRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.Model = "echo"
	}
	if body.Model == "" {
		body.Model = "echo"
	}
	content := "ok"
	if isConsolidateMode(r) {
		content = consolidateDigest(body.Messages)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(newCompletion(body.Model, content))
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	// Register both the default and the consolidate-suffix routes so a
	// path-routed activation works WITHOUT relying on net/http's prefix
	// matching (which would forward /v1/chat/completions/consolidate to
	// /v1/chat/completions only if the latter is a prefix handler — it
	// isn't, by design, to keep accidental routes off the surface).
	mux.HandleFunc("/v1/chat/completions", handleChat)
	mux.HandleFunc("/v1/chat/completions/consolidate", handleChat)
	// Webhook delivery sink for the monitoring scenarios (echo-sink.go). Keeps
	// every notification the alert tests provoke inside this container.
	registerSink(mux)
	log.Println("echo-llm listening on :8080")
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("echo-llm: %v", err)
	}
}
