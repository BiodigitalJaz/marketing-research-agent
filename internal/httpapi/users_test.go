package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

const tempPassword = "temporary password 123"

func memberID(t *testing.T, c *client, email string) string {
	t.Helper()
	_, body := c.do(http.MethodGet, "/settings/users", nil)
	var out struct {
		Items []struct{ ID, Email, Role string }
	}
	_ = json.Unmarshal(body, &out)
	for _, m := range out.Items {
		if m.Email == email {
			return m.ID
		}
	}
	t.Fatalf("%s not listed: %s", email, body)
	return ""
}

func TestManageMembers(t *testing.T) {
	e := newEnv(t)
	ws := e.workspace("Benchwright")
	e.user("admin@example.com", ws, store.MemberRoleAdmin)
	e.user("viewer@example.com", ws, store.MemberRoleViewer)
	admin := e.client()
	admin.login("admin@example.com")

	viewer := e.client()
	viewer.login("viewer@example.com")
	if code, _ := viewer.do(http.MethodGet, "/settings/users", nil); code != http.StatusForbidden {
		t.Fatalf("viewer listing users = %d", code)
	}

	// Add a new person with a temporary password; they can sign in with it.
	add := map[string]string{"email": "New@Example.com", "name": "New", "role": "editor", "password": "short"}
	if code, _ := admin.do(http.MethodPost, "/settings/users", add); code != http.StatusBadRequest {
		t.Fatalf("short temp password = %d", code)
	}
	add["password"] = tempPassword
	code, body := admin.do(http.MethodPost, "/settings/users", add)
	if code != http.StatusCreated {
		t.Fatalf("add = %d %s", code, body)
	}
	if code, body := admin.do(http.MethodPost, "/settings/users", add); code != http.StatusConflict || errCode(body) != "already_member" {
		t.Fatalf("add twice = %d %s", code, body)
	}
	newbie := e.client()
	if code, body := newbie.do(http.MethodPost, "/auth/login", map[string]string{"email": "new@example.com", "password": tempPassword}); code != http.StatusOK {
		t.Fatalf("new member login = %d %s", code, body)
	}
	var s struct {
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.Unmarshal(body, &s)
	newbie.csrf = s.CSRFToken

	// Roles change; the last admin cannot be demoted or removed.
	newID := memberID(t, admin, "new@example.com")
	adminID := memberID(t, admin, "admin@example.com")
	if code, body := admin.do(http.MethodPut, "/settings/users/"+newID, map[string]string{"role": "viewer"}); code != http.StatusOK {
		t.Fatalf("role change = %d %s", code, body)
	}
	if code, body := admin.do(http.MethodPut, "/settings/users/"+adminID, map[string]string{"role": "editor"}); code != http.StatusConflict || errCode(body) != "last_admin" {
		t.Fatalf("demote last admin = %d %s", code, body)
	}
	if code, body := admin.do(http.MethodDelete, "/settings/users/"+adminID, nil); code != http.StatusConflict || errCode(body) != "last_admin" {
		t.Fatalf("remove last admin = %d %s", code, body)
	}

	// A reset sets a new password and signs the person out.
	if code, body := admin.do(http.MethodPost, "/settings/users/"+newID+"/password", map[string]string{"password": "another long password"}); code != http.StatusNoContent {
		t.Fatalf("reset = %d %s", code, body)
	}
	if code, _ := newbie.do(http.MethodGet, "/auth/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("session should end after a reset: %d", code)
	}

	// Someone who also belongs to another workspace keeps control of their
	// password.
	other := e.workspace("Other")
	if err := e.q.AddMembership(context.Background(), store.AddMembershipParams{WorkspaceID: other, UserID: uuidOf(t, e, "viewer@example.com"), Role: store.MemberRoleViewer}); err != nil {
		t.Fatal(err)
	}
	viewerID := memberID(t, admin, "viewer@example.com")
	if code, body := admin.do(http.MethodPost, "/settings/users/"+viewerID+"/password", map[string]string{"password": "another long password"}); code != http.StatusForbidden || errCode(body) != "other_workspaces" {
		t.Fatalf("reset across workspaces = %d %s", code, body)
	}
	// Removing them here keeps their session: they still belong elsewhere.
	if code, _ := admin.do(http.MethodDelete, "/settings/users/"+viewerID, nil); code != http.StatusNoContent {
		t.Fatalf("remove = %d", code)
	}
	if code, _ := viewer.do(http.MethodGet, "/auth/me", nil); code != http.StatusOK {
		t.Fatal("a member of another workspace should stay signed in")
	}
	// Removing the new member, who belongs nowhere else, ends their sessions.
	newbie2 := e.client()
	if code, _ := newbie2.do(http.MethodPost, "/auth/login", map[string]string{"email": "new@example.com", "password": "another long password"}); code != http.StatusOK {
		t.Fatal("login with the reset password failed")
	}
	if code, _ := admin.do(http.MethodDelete, "/settings/users/"+newID, nil); code != http.StatusNoContent {
		t.Fatalf("remove new = %d", code)
	}
	if code, _ := newbie2.do(http.MethodGet, "/auth/me", nil); code != http.StatusUnauthorized {
		t.Fatal("removed member should be signed out")
	}
	var audits int
	_ = e.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE workspace_id = $1 AND action LIKE 'member.%'", ws).Scan(&audits)
	if audits < 5 {
		t.Errorf("member audit rows = %d", audits)
	}
}

func uuidOf(t *testing.T, e *env, email string) uuid.UUID {
	t.Helper()
	u, err := e.q.GetUserByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestChangeOwnPasswordAndRenameWorkspace(t *testing.T) {
	e := newEnv(t)
	ws := e.workspace("Benchwright")
	e.user("admin@example.com", ws, store.MemberRoleAdmin)
	a, b := e.client(), e.client()
	a.login("admin@example.com")
	b.login("admin@example.com")

	if code, body := a.do(http.MethodPost, "/auth/password", map[string]string{"current_password": "wrong", "new_password": "a brand new password"}); code != http.StatusUnauthorized || errCode(body) != "invalid_credentials" {
		t.Fatalf("wrong current = %d %s", code, body)
	}
	if code, body := a.do(http.MethodPost, "/auth/password", map[string]string{"current_password": testPassword, "new_password": "a brand new password"}); code != http.StatusNoContent {
		t.Fatalf("change = %d %s", code, body)
	}
	if code, _ := a.do(http.MethodGet, "/auth/me", nil); code != http.StatusOK {
		t.Fatal("the current session should survive a password change")
	}
	if code, _ := b.do(http.MethodGet, "/auth/me", nil); code != http.StatusUnauthorized {
		t.Fatal("other sessions should end after a password change")
	}

	if code, _ := a.do(http.MethodPut, "/settings/workspace/name", map[string]string{"name": "  "}); code != http.StatusBadRequest {
		t.Fatalf("blank name = %d", code)
	}
	if code, body := a.do(http.MethodPut, "/settings/workspace/name", map[string]string{"name": "Benchwright Labs"}); code != http.StatusOK {
		t.Fatalf("rename = %d %s", code, body)
	}
	_, body := a.do(http.MethodGet, "/auth/me", nil)
	var me struct{ Workspaces []struct{ Name string } }
	_ = json.Unmarshal(body, &me)
	if len(me.Workspaces) != 1 || me.Workspaces[0].Name != "Benchwright Labs" {
		t.Errorf("workspaces = %+v", me.Workspaces)
	}
}
