package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

func TestDeletedSourcesLeaveMonitors(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ws := e.workspace("Benchwright")
	e.user("admin@example.com", ws, store.MemberRoleAdmin)
	c := e.client()
	c.login("admin@example.com")

	newSource := func(name string) uuid.UUID {
		t.Helper()
		s, err := e.q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws, Connector: "rss", Name: name, Config: []byte(`{"feed_url":"https://example.com/` + name + `"}`), Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	keep, gone := newSource("keep"), newSource("gone")
	code, body := c.do(http.MethodPost, "/monitors", map[string]any{"name": "M", "source_ids": []uuid.UUID{keep, gone}, "schedule_cron": "@hourly"})
	if code != http.StatusCreated {
		t.Fatalf("create monitor = %d %s", code, body)
	}
	var mon struct{ ID uuid.UUID }
	_ = json.Unmarshal(body, &mon)

	// Deleting a source removes it from the monitor.
	if code, _ := c.do(http.MethodDelete, "/sources/"+gone.String(), nil); code != http.StatusNoContent {
		t.Fatalf("delete source = %d", code)
	}
	m, _ := e.q.GetMonitor(ctx, store.GetMonitorParams{WorkspaceID: ws, ID: mon.ID})
	if len(m.SourceIds) != 1 || m.SourceIds[0] != keep {
		t.Fatalf("monitor sources after delete = %v", m.SourceIds)
	}

	// A save that still carries a stale id keeps only the real sources.
	stale := uuid.New()
	code, body = c.do(http.MethodPut, "/monitors/"+mon.ID.String(), map[string]any{"name": "M", "source_ids": []uuid.UUID{keep, stale}, "schedule_cron": "@hourly", "enabled": true})
	if code != http.StatusOK {
		t.Fatalf("save with a stale id = %d %s", code, body)
	}
	m, _ = e.q.GetMonitor(ctx, store.GetMonitorParams{WorkspaceID: ws, ID: mon.ID})
	if len(m.SourceIds) != 1 || m.SourceIds[0] != keep {
		t.Fatalf("stale id saved: %v", m.SourceIds)
	}

	// Only unknown (or another workspace's) sources: refused.
	other := e.workspace("Other")
	foreign, err := e.q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: other, Connector: "rss", Name: "x", Config: []byte(`{"feed_url":"https://example.com/x"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := c.do(http.MethodPut, "/monitors/"+mon.ID.String(), map[string]any{"name": "M", "source_ids": []uuid.UUID{foreign.ID}, "schedule_cron": "@hourly"}); code != http.StatusBadRequest {
		t.Fatalf("save with another workspace's source = %d %s", code, body)
	}
}
