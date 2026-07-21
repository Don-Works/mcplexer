package admin

import "strings"

// Task shapes describe the structural scale/topology of the delegated
// work (how much of the repo it spans), complementing task_kind, which is
// a reviewer-scored skill axis. Shapes are persisted on delegations and
// model stats so shape-keyed routing history can accumulate.
const (
	taskShapeReview   = "review"
	taskShapeResearch = "research"
	taskShapeScan     = "codebase_scan"
	taskShapeSmall    = "small_edit"
	taskShapeMulti    = "multi_file"
)

var knownTaskShapes = map[string]struct{}{
	taskShapeReview:   {},
	taskShapeResearch: {},
	taskShapeScan:     {},
	taskShapeSmall:    {},
	taskShapeMulti:    {},
}

// normalizeTaskShape canonicalizes a caller-supplied shape; unknown
// values yield "" so the classifier decides instead.
func normalizeTaskShape(shape string) string {
	shape = strings.ReplaceAll(strings.ReplaceAll(
		strings.ToLower(strings.TrimSpace(shape)), "-", "_"), " ", "_")
	if _, ok := knownTaskShapes[shape]; ok {
		return shape
	}
	return ""
}

// classifyTaskShape derives a task shape from the objective and handoff.
// Keyword classes are checked most-specific first: explicit breadth
// signals (whole-repo scan, cross-cutting multi-file) must win before the
// length fallbacks, or a short "refactor across files" objective would
// misclassify as a small edit.
func classifyTaskShape(objective, handoff string) string {
	combined := strings.ToLower(objective + " " + handoff)
	reviewKW := []string{"review", "critique", "audit", "inspect", "check", "assess", "evaluate"}
	researchKW := []string{"research", "investigate", "analyse", "analyze", "survey", "study", "explore", "understand", "learn"}
	scanKW := []string{"codebase", "whole repo", "entire repo", "all files", "search across", "broad", "sweep", "inventory", "catalog", "overview"}
	multiKW := []string{"refactor", "restructure", "migrate", "multiple files", "across files", "multi-file", "several files", "many files", "cross-cutting"}
	editKW := []string{"fix", "bug", "patch", "quick", "rename", "small", "tiny", "one file", "single file", "simple", "minor", "typo", "trivial"}
	codeKW := []string{"implement", "add", "create", "build", "write", "code", "change", "modify", "update", "edit", "fix", "refactor", "migrate"}
	has := func(kws []string) bool {
		for _, kw := range kws {
			if strings.Contains(combined, kw) {
				return true
			}
		}
		return false
	}
	l := len(objective) + len(handoff)
	switch {
	case has(reviewKW) && !has(codeKW):
		return taskShapeReview
	case has(researchKW) && !has(codeKW):
		return taskShapeResearch
	case has(scanKW):
		return taskShapeScan
	case has(multiKW):
		return taskShapeMulti
	case has(editKW) && l < 300:
		return taskShapeSmall
	case l < 100:
		return taskShapeSmall
	case has(codeKW) && l >= 300:
		return taskShapeMulti
	case has(codeKW):
		return taskShapeSmall
	default:
		return ""
	}
}

// inferredTaskKindForShape maps a shape onto the task_kind vocabulary the
// existing ranking pipeline (x2 review weight, category EWMA, capability
// bonus) is keyed on. Used only when the caller supplied no task_kind, so
// shape inference lights up kind-aware routing without a parallel scoring
// path; the inference is recorded as provenance and a parent review's
// task_kind override remains the correction mechanism.
func inferredTaskKindForShape(shape string) string {
	switch shape {
	case taskShapeReview:
		return "review"
	case taskShapeResearch, taskShapeScan:
		return "research"
	case taskShapeSmall, taskShapeMulti:
		return "coding"
	default:
		return ""
	}
}
