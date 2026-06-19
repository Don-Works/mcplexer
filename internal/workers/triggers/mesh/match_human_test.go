package mesh

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestMatchesTrigger_HumanTagDoesNotMatchWorkerOutput(t *testing.T) {
	t.Parallel()
	trig := &store.WorkerMeshTrigger{TagMatch: "human", Enabled: true}
	inbound := &store.MeshMessage{Kind: "task", Tags: "human,telegram"}
	output := &store.MeshMessage{Kind: "finding", Tags: "worker,output,telegram,chain-depth:2"}

	if !matchesTrigger(trig, inbound) {
		t.Fatal("human-tagged inbound should match human trigger")
	}
	if matchesTrigger(trig, output) {
		t.Fatal("worker output must not match human-only trigger")
	}
}

func TestMatchesTrigger_LegacyTelegramTagMatchesWorkerOutput(t *testing.T) {
	t.Parallel()
	trig := &store.WorkerMeshTrigger{TagMatch: "telegram", Enabled: true}
	output := &store.MeshMessage{Kind: "finding", Tags: "worker,output,telegram,chain-depth:2"}

	if !matchesTrigger(trig, output) {
		t.Fatal("legacy telegram trigger should match worker output")
	}
}
