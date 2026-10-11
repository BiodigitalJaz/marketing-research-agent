package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

func TestWorkspaceMembership(t *testing.T) {
	ctx := context.Background()
	q := store.New(pgtest.NewMigratedDB(t))

	ws, err := q.CreateWorkspace(ctx, "Benchwright")
	if err != nil {
		t.Fatal(err)
	}
	other, err := q.CreateWorkspace(ctx, "Other")
	if err != nil {
		t.Fatal(err)
	}
	u, err := q.CreateUser(ctx, store.CreateUserParams{Email: "Admin@Example.com", Name: "Admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.AddMembership(ctx, store.AddMembershipParams{WorkspaceID: ws.ID, UserID: u.ID, Role: store.MemberRoleAdmin}); err != nil {
		t.Fatal(err)
	}

	// citext: email lookup is case-insensitive.
	got, err := q.GetUserByEmail(ctx, "admin@example.com")
	if err != nil || got.ID != u.ID {
		t.Fatalf("GetUserByEmail: %v %v", got.ID, err)
	}

	list, err := q.ListWorkspacesForUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != ws.ID || list[0].Role != store.MemberRoleAdmin {
		t.Fatalf("ListWorkspacesForUser = %+v", list)
	}

	if _, err := q.GetMembership(ctx, store.GetMembershipParams{WorkspaceID: other.ID, UserID: u.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected no membership in other workspace, got %v", err)
	}
}
