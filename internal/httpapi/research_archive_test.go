package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

func TestArchiveAndDeleteResearch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ws := e.workspace("Benchwright")
	e.user("ed@example.com", ws, store.MemberRoleEditor)
	e.user("other@example.com", ws, store.MemberRoleEditor)
	e.user("admin@example.com", ws, store.MemberRoleAdmin)
	ed, other, admin := e.client(), e.client(), e.client()
	ed.login("ed@example.com")
	other.login("other@example.com")
	admin.login("admin@example.com")

	userID := func(email string) uuid.UUID {
		u, err := e.q.GetUserByEmail(ctx, email)
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	job := func(by, status string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := e.pool.QueryRow(ctx, `INSERT INTO research_jobs (workspace_id, created_by, mode, question, status)
			VALUES ($1, $2, 'chat', 'q', $3::job_status) RETURNING id`, ws, userID(by), status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	list := func(archived bool) []string {
		t.Helper()
		path := "/research"
		if archived {
			path += "?archived=true"
		}
		_, body := ed.do(http.MethodGet, path, nil)
		var out struct{ Items []struct{ ID string } }
		_ = json.Unmarshal(body, &out)
		ids := []string{}
		for _, it := range out.Items {
			ids = append(ids, it.ID)
		}
		return ids
	}

	done, running := job("ed@example.com", "succeeded"), job("ed@example.com", "running")
	var report uuid.UUID
	if err := e.pool.QueryRow(ctx, "INSERT INTO reports (workspace_id, job_id, title, markdown) VALUES ($1, $2, 'R', 'x') RETURNING id", ws, done).Scan(&report); err != nil {
		t.Fatal(err)
	}

	// Archive hides a job from the list; unarchive restores it.
	if code, _ := ed.do(http.MethodPost, "/research/"+done.String()+"/archive", map[string]bool{"archived": true}); code != http.StatusNoContent {
		t.Fatalf("archive = %d", code)
	}
	if got := list(false); len(got) != 1 || got[0] != running.String() {
		t.Errorf("active list = %v", got)
	}
	if got := list(true); len(got) != 1 || got[0] != done.String() {
		t.Errorf("archived list = %v", got)
	}
	ed.do(http.MethodPost, "/research/"+done.String()+"/archive", map[string]bool{"archived": false})
	if got := list(false); len(got) != 2 {
		t.Errorf("after unarchive = %v", got)
	}

	// Deleting: running jobs are refused; only the author or an admin may delete.
	if code, body := ed.do(http.MethodDelete, "/research/"+running.String(), nil); code != http.StatusConflict || errCode(body) != "research_running" {
		t.Fatalf("delete running = %d %s", code, body)
	}
	if code, _ := other.do(http.MethodDelete, "/research/"+done.String(), nil); code != http.StatusForbidden {
		t.Fatalf("another editor deleting = %d", code)
	}
	if code, _ := ed.do(http.MethodDelete, "/research/"+done.String(), nil); code != http.StatusNoContent {
		t.Fatalf("author delete = %d", code)
	}
	var jobRef *uuid.UUID
	if err := e.pool.QueryRow(ctx, "SELECT job_id FROM reports WHERE id = $1", report).Scan(&jobRef); err != nil || jobRef != nil {
		t.Errorf("the report should remain, unlinked: job_id=%v err=%v", jobRef, err)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE research_jobs SET status = 'failed' WHERE id = $1", running); err != nil {
		t.Fatal(err)
	}
	if code, _ := admin.do(http.MethodDelete, "/research/"+running.String(), nil); code != http.StatusNoContent {
		t.Fatalf("admin delete = %d", code)
	}
}
