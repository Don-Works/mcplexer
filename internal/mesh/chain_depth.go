// chain_depth.go — shared parser for the "chain-depth:N" tag that
// guards mesh-event loops.
//
// The convention started in internal/workers/triggers/mesh/match.go
// where the dispatcher reads + increments depth on each worker fire
// (see PLAN.md "Chain-depth propagation"). Phase 2 of tasks needs the
// same primitive from internal/tasks/events.go — a task that fires
// because of an incoming worker_finding mesh message must stamp the
// next depth on its task_event:* emission, otherwise the worker that
// re-triggers on task_event:* bypasses MaxChainDepth.
//
// We promote the helpers here so both packages depend on the same
// implementation rather than re-deriving it. workers/triggers/mesh's
// private helpers forward to these.

package mesh

import (
	"fmt"
	"strconv"
	"strings"
)

// ChainDepthFromTags reads "chain-depth:<N>" off a comma-separated
// tag string. Missing, malformed, or non-positive entries → 0.
func ChainDepthFromTags(tags string) int {
	if tags == "" {
		return 0
	}
	for _, raw := range strings.Split(tags, ",") {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		rest, ok := strings.CutPrefix(tag, "chain-depth:")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// ChainDepthTag renders the depth fragment a downstream emitter
// should stamp on its mesh send so the next layer's loop guard sees
// the right depth. Returns "" for non-positive depths so callers can
// safely concatenate without producing a "chain-depth:0" no-op.
func ChainDepthTag(depth int) string {
	if depth <= 0 {
		return ""
	}
	return "chain-depth:" + fmt.Sprintf("%d", depth)
}
