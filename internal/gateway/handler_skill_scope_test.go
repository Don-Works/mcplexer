package gateway

import "testing"

func TestNormalizeSkillPublishWorkspaceNeverPersistsGlobalSentinel(t *testing.T) {
	tests := []struct {
		name      string
		scope     string
		workspace string
		wantNil   bool
		wantErr   bool
	}{
		{name: "auto global sentinel", scope: "auto", workspace: globalWorkspaceID, wantNil: true},
		{name: "empty mode global sentinel", workspace: globalWorkspaceID, wantNil: true},
		{name: "forced global", scope: "global", workspace: "workspace-one", wantNil: true},
		{name: "workspace sentinel rejected", scope: "workspace", workspace: globalWorkspaceID, wantErr: true},
		{name: "real workspace", scope: "workspace", workspace: "workspace-one"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeSkillPublishWorkspace(test.scope, test.workspace)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if (got == nil) != test.wantNil {
				t.Fatalf("workspace = %v, wantNil %v", got, test.wantNil)
			}
			if got != nil && *got == globalWorkspaceID {
				t.Fatal("routing sentinel became a persisted registry workspace ID")
			}
		})
	}
}
