package knowledge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/embed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Limits on what is read from the repository.
const (
	MaxDocBytes = 1 << 20 // larger files are skipped
	embedBatch  = 16
	maxEmbedLen = 4000
)

// Service syncs knowledge sources and builds the memory bank.
type Service struct {
	Pool     *pgxpool.Pool
	Get      Getter
	Secrets  *secrets.Box
	Env      func(string) string
	Embedder func(ctx context.Context, ws uuid.UUID) (embed.Embedder, error)
	Router   Router // memory bank generation; nil disables it
	Log      *slog.Logger
	APIBase  string // GitHub API base (tests)
}

// SyncResult reports what a sync changed.
type SyncResult struct {
	Commit   string
	Changed  int // docs added or updated
	Removed  int
	Skipped  []string
	Embedded int
}

func (s *Service) client(ctx context.Context, src store.KnowledgeSource) (*GitHub, error) {
	token := ""
	switch {
	case src.TokenSecretID != nil:
		sec, err := store.New(s.Pool).GetSecretAnyScope(ctx, store.GetSecretAnyScopeParams{ID: *src.TokenSecretID, WorkspaceID: src.WorkspaceID})
		if err != nil {
			return nil, fmt.Errorf("token secret: %w", err)
		}
		token, err = s.Secrets.Open(secrets.Sealed{Ciphertext: sec.Ciphertext, Nonce: sec.Nonce, KeyID: sec.KeyID}, sec.ID, sec.WorkspaceID)
		if err != nil {
			return nil, fmt.Errorf("token secret: %w", err)
		}
	case src.TokenEnv != nil && *src.TokenEnv != "" && s.Env != nil:
		token = s.Env(*src.TokenEnv)
	}
	return &GitHub{Base: s.APIBase, Token: token, Get: s.Get}, nil
}

// Sync brings a source up to date with its branch: only files whose blob
// changed are re-read, removed files are dropped, and new sections are
// embedded. An unchanged branch costs one API request (plus embedding any
// sections that could not be embedded before). Unchanged files are never
// rewritten, so a sync never discards memory bank work. It returns ErrBusy
// while another sync or a build runs for the workspace.
func (s *Service) Sync(ctx context.Context, src store.KnowledgeSource) (*SyncResult, error) {
	var res *SyncResult
	err := s.withLock(ctx, src.WorkspaceID, func() error {
		var err error
		res, err = s.syncLocked(ctx, src)
		return err
	})
	return res, err
}

func (s *Service) syncLocked(ctx context.Context, src store.KnowledgeSource) (*SyncResult, error) {
	q := store.New(s.Pool)
	ws := src.WorkspaceID
	_ = q.SetKnowledgeSourceStatus(ctx, store.SetKnowledgeSourceStatusParams{WorkspaceID: ws, ID: src.ID, Status: "syncing", LastError: src.LastError})
	res, err := s.sync(ctx, q, src)
	commit := ""
	if res != nil {
		commit = res.Commit
	}
	if commit == "" && src.LastCommit != nil {
		commit = *src.LastCommit
	}
	var msg *string
	status := "idle"
	if err != nil {
		m := err.Error()
		msg, status = &m, "error"
	}
	var last *string
	if commit != "" {
		last = &commit
	}
	if rerr := q.RecordKnowledgeSync(context.WithoutCancel(ctx), store.RecordKnowledgeSyncParams{WorkspaceID: ws, ID: src.ID, LastCommit: last, Status: status, LastError: msg}); rerr != nil {
		return res, rerr
	}
	return res, err
}

func (s *Service) sync(ctx context.Context, q *store.Queries, src store.KnowledgeSource) (*SyncResult, error) {
	gh, err := s.client(ctx, src)
	if err != nil {
		return nil, err
	}
	head, err := gh.Head(ctx, src.Repo, src.Branch)
	if err != nil {
		return nil, err
	}
	res := &SyncResult{Commit: head}
	if src.LastCommit != nil && *src.LastCommit == head {
		res.Embedded, err = s.EmbedMissing(ctx, src.WorkspaceID)
		return res, err
	}
	files, err := gh.Files(ctx, src.Repo, head, src.PathPrefix)
	if err != nil {
		return nil, err
	}
	prefix := strings.Trim(src.PathPrefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	have := map[string]string{}
	rows, err := q.KnowledgeDocBlobs(ctx, store.KnowledgeDocBlobsParams{WorkspaceID: src.WorkspaceID, SourceID: src.ID})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		have[r.Path] = r.BlobSha
	}
	keep := []string{}
	for _, f := range files {
		rel := strings.TrimPrefix(f.Path, prefix)
		if !strings.EqualFold(pathExt(f.Path), ".md") || Excluded(rel, src.Exclude) {
			continue
		}
		if f.Size > MaxDocBytes {
			res.Skipped = append(res.Skipped, f.Path+" (too large)")
			continue
		}
		keep = append(keep, f.Path)
		if have[f.Path] == f.SHA {
			continue
		}
		raw, err := gh.Blob(ctx, src.Repo, f.SHA)
		if err != nil {
			return res, fmt.Errorf("read %s: %w", f.Path, err)
		}
		if !utf8.Valid(raw) {
			res.Skipped = append(res.Skipped, f.Path+" (not UTF-8)")
			continue
		}
		if err := s.storeDoc(ctx, src, f, head, string(raw)); err != nil {
			return res, err
		}
		res.Changed++
	}
	removed, err := q.DeleteKnowledgeDocsNotIn(ctx, store.DeleteKnowledgeDocsNotInParams{WorkspaceID: src.WorkspaceID, SourceID: src.ID, Keep: keep})
	if err != nil {
		return res, err
	}
	res.Removed = len(removed)
	if err := q.TouchKnowledgeDocCommit(ctx, store.TouchKnowledgeDocCommitParams{WorkspaceID: src.WorkspaceID, SourceID: src.ID, CommitSha: head}); err != nil {
		return res, err
	}
	if err := q.DeleteOrphanKnowledgeEmbeddings(ctx, src.WorkspaceID); err != nil {
		return res, err
	}
	res.Embedded, err = s.EmbedMissing(ctx, src.WorkspaceID)
	return res, err
}

// storeDoc replaces a document and its sections in one transaction.
func (s *Service) storeDoc(ctx context.Context, src store.KnowledgeSource, f TreeEntry, commit, content string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		id, err := q.UpsertKnowledgeDoc(ctx, store.UpsertKnowledgeDocParams{WorkspaceID: src.WorkspaceID, SourceID: src.ID, Path: f.Path,
			BlobSha: f.SHA, CommitSha: commit, Title: Title(f.Path, content), Content: content})
		if err != nil {
			return fmt.Errorf("store %s: %w", f.Path, err)
		}
		if _, err := q.DeleteKnowledgeChunks(ctx, store.DeleteKnowledgeChunksParams{WorkspaceID: src.WorkspaceID, DocID: id}); err != nil {
			return err
		}
		for i, c := range Split(f.Path, content) {
			if err := q.InsertKnowledgeChunk(ctx, store.InsertKnowledgeChunkParams{WorkspaceID: src.WorkspaceID, DocID: id, Ord: int32(i), // #nosec G115 -- small
				Heading: c.Heading, Anchor: c.Anchor, Content: c.Content}); err != nil {
				return err
			}
		}
		return nil
	})
}

// EmbedMissing embeds sections that have no embedding for the workspace's
// current model. Without an embedder it does nothing (keyword search still
// works).
func (s *Service) EmbedMissing(ctx context.Context, ws uuid.UUID) (int, error) {
	if s.Embedder == nil {
		return 0, nil
	}
	e, err := s.Embedder(ctx, ws)
	if errors.Is(err, embed.ErrNotConfigured) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	q := store.New(s.Pool)
	done := 0
	for {
		rows, err := q.ChunksMissingEmbedding(ctx, store.ChunksMissingEmbeddingParams{WorkspaceID: ws, Model: e.Model(), Lim: embedBatch})
		if err != nil || len(rows) == 0 {
			return done, err
		}
		texts := make([]string, len(rows))
		for i, r := range rows {
			texts[i] = clip(r.Heading+"\n\n"+r.Content, maxEmbedLen)
		}
		vecs, err := e.Embed(ctx, texts)
		if err != nil {
			return done, fmt.Errorf("embed knowledge: %w", err)
		}
		for i, r := range rows {
			if err := q.UpsertEmbedding(ctx, store.UpsertEmbeddingParams{EntityType: "knowledge_chunk", EntityID: r.ID, Model: e.Model(),
				Dims: int32(e.Dims()), Embedding: pgvector.NewVector(vecs[i]), WorkspaceID: ws}); err != nil { // #nosec G115 -- small
				return done, err
			}
		}
		done += len(rows)
	}
}

func pathExt(p string) string {
	i := strings.LastIndexByte(p, '.')
	if i < 0 || strings.Contains(p[i:], "/") {
		return ""
	}
	return p[i:]
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
