package jobs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/embed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

func fakeEmbeddings(t *testing.T, dims int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []item
		for i := range req.Input {
			v := make([]float32, dims)
			v[i%dims] = 1
			data = append(data, item{i, v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEmbedEntityStoresVector(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	log := pgtest.DiscardLogger()
	q := store.New(pool)

	ws, _ := q.CreateWorkspace(ctx, "A")
	item, err := q.CreatePortfolioItem(ctx, store.CreatePortfolioItemParams{
		WorkspaceID: ws.ID, Kind: "blog_post", Title: "Guardrails", Summary: "How the risk engine refuses", Tags: []byte(`{"problems":["safety"]}`)})
	if err != nil {
		t.Fatal(err)
	}

	srv := fakeEmbeddings(t, 768)
	emb := embed.NewOpenAICompat(srv.URL+"/v1", "", "nomic-embed-text", 768)
	client, err := jobs.NewWorkerClient(jobs.Deps{Log: log, Pool: pool, Embedder: jobs.StaticEmbedder(emb)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	done, cancel := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cc := context.WithTimeout(context.Background(), 10*time.Second)
		defer cc()
		_ = client.Stop(c)
	})

	if _, err := client.Insert(ctx, jobs.EmbedEntityArgs{WorkspaceID: ws.ID, EntityType: jobs.EntityPortfolioItem, EntityID: item.ID}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev := <-done:
			if ev.Job.Kind != "embed_entity" {
				continue
			}
			if ev.Kind == river.EventKindJobFailed {
				t.Fatalf("embed job failed: %v", ev.Job.Errors)
			}
			n, err := q.CountEmbeddings(ctx, store.CountEmbeddingsParams{WorkspaceID: ws.ID, EntityType: jobs.EntityPortfolioItem, Model: "nomic-embed-text"})
			if err != nil || n != 1 {
				t.Fatalf("embeddings = %d, %v", n, err)
			}
			// The stored vector is usable by the per-model HNSW index query shape.
			var id string
			err = pool.QueryRow(ctx, `SELECT entity_id::text FROM embeddings WHERE model = 'nomic-embed-text'
				ORDER BY embedding::vector(768) <=> (SELECT embedding FROM embeddings LIMIT 1)::vector(768) LIMIT 1`).Scan(&id)
			if err != nil || id != item.ID.String() {
				t.Fatalf("similarity query: %s %v", id, err)
			}
			return
		case <-deadline:
			t.Fatal("embed job did not finish")
		}
	}
}

func TestEmbedEntityCancelledWithoutEmbedder(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	client, err := jobs.NewWorkerClient(jobs.Deps{Log: pgtest.DiscardLogger(), Pool: pool}, 1)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := client.Subscribe(river.EventKindJobCancelled)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cc := context.WithTimeout(context.Background(), 10*time.Second)
		defer cc()
		_ = client.Stop(c)
	})
	if _, err := client.Insert(ctx, jobs.EmbedEntityArgs{EntityType: jobs.EntityICP}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(15 * time.Second):
		t.Fatal("expected embed job to be cancelled when no embedder is configured")
	}
}
