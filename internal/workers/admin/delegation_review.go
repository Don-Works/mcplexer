package admin

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

func normaliseDelegationTaskKind(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	v = strings.ReplaceAll(v, "-", "_")
	v = strings.ReplaceAll(v, " ", "_")
	return v
}

func normaliseDelegationReviewScores(raw map[string]int) (map[string]int, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]int, len(raw))
	for key, score := range raw {
		k := normaliseDelegationTaskKind(key)
		if k == "" {
			return nil, errors.New("review score category cannot be empty")
		}
		if score < 0 || score > 100 {
			return nil, fmt.Errorf("review score %q must be 0..100", k)
		}
		out[k] = score
	}
	return out, nil
}

func normaliseDelegationModelReviews(raw []DelegationModelReview) ([]DelegationModelReview, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]DelegationModelReview, 0, len(raw))
	for i, review := range raw {
		review.ModelKey = strings.TrimSpace(review.ModelKey)
		review.WorkerID = strings.TrimSpace(review.WorkerID)
		review.Outcome = normaliseDelegationOutcome(review.Outcome, review.Score)
		review.Notes = strings.TrimSpace(review.Notes)
		if review.ModelKey == "" && review.WorkerID == "" {
			return nil, fmt.Errorf("model_scores[%d] requires model_key or worker_id", i)
		}
		if review.Score < 0 || review.Score > 100 {
			return nil, fmt.Errorf("model_scores[%d].score must be 0..100", i)
		}
		scores, err := normaliseDelegationReviewScores(review.Scores)
		if err != nil {
			return nil, fmt.Errorf("model_scores[%d]: %w", i, err)
		}
		review.Scores = scores
		out = append(out, review)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModelKey == out[j].ModelKey {
			return out[i].WorkerID < out[j].WorkerID
		}
		return out[i].ModelKey < out[j].ModelKey
	})
	return out, nil
}

func reviewForDelegationModel(
	review DelegationReview,
	modelKey string,
	workerIDs []string,
) DelegationModelReview {
	for _, row := range review.ModelScores {
		if row.ModelKey != "" && row.ModelKey == modelKey {
			return row
		}
		if row.WorkerID != "" && stringSliceContains(workerIDs, row.WorkerID) {
			return row
		}
	}
	return DelegationModelReview{
		ModelKey: modelKey,
		Score:    review.Score,
		Outcome:  review.Outcome,
		Notes:    review.Notes,
		Scores:   review.Scores,
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
