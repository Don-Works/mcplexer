package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestPersonWorkspaceScopeAndNameUniqueness(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if _, err := db.GetWorkspace(ctx, store.PersonDefaultWorkspaceID); err != nil {
		t.Fatalf("default crm workspace missing: %v", err)
	}
	if err := db.CreateWorkspace(ctx, &store.Workspace{ID: "sales", Name: "Sales"}); err != nil {
		t.Fatalf("create sales workspace: %v", err)
	}

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	crmPerson := &store.PersonEntry{
		ID:          "person-crm",
		WorkspaceID: store.PersonDefaultWorkspaceID,
		Name:        "Alex Smith",
		Company:     "CRM Co",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	salesPerson := &store.PersonEntry{
		ID:          "person-sales",
		WorkspaceID: "sales",
		Name:        "Alex Smith",
		Company:     "Sales Co",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := db.WritePerson(ctx, crmPerson); err != nil {
		t.Fatalf("write crm person: %v", err)
	}
	if err := db.WritePerson(ctx, salesPerson); err != nil {
		t.Fatalf("write sales person with same name: %v", err)
	}
	if err := db.WritePerson(ctx, &store.PersonEntry{
		ID:          "person-crm-dupe",
		WorkspaceID: store.PersonDefaultWorkspaceID,
		Name:        "Alex Smith",
		CreatedAt:   now,
		UpdatedAt:   now,
	}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("same-workspace duplicate err = %v, want ErrAlreadyExists", err)
	}

	crmRows, err := db.ListPeople(ctx, store.PersonFilter{WorkspaceID: store.PersonDefaultWorkspaceID})
	if err != nil {
		t.Fatalf("list crm people: %v", err)
	}
	if len(crmRows) != 1 || crmRows[0].ID != crmPerson.ID || crmRows[0].WorkspaceID != store.PersonDefaultWorkspaceID {
		t.Fatalf("crm rows leaked or missing: %+v", crmRows)
	}
	salesRows, err := db.ListPeople(ctx, store.PersonFilter{WorkspaceID: "sales"})
	if err != nil {
		t.Fatalf("list sales people: %v", err)
	}
	if len(salesRows) != 1 || salesRows[0].ID != salesPerson.ID || salesRows[0].WorkspaceID != "sales" {
		t.Fatalf("sales rows leaked or missing: %+v", salesRows)
	}
}
