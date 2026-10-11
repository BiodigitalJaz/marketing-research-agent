package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/knowledge"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type knowledgeSourceDTO struct {
	ID           uuid.UUID    `json:"id"`
	Name         string       `json:"name"`
	Repo         string       `json:"repo"`
	Branch       string       `json:"branch"`
	PathPrefix   string       `json:"path_prefix"`
	Exclude      []string     `json:"exclude"`
	Token        apiKeyStatus `json:"token"`
	Enabled      bool         `json:"enabled"`
	LastCommit   *string      `json:"last_commit"`
	LastSyncedAt *time.Time   `json:"last_synced_at"`
	LastError    *string      `json:"last_error"`
	Status       string       `json:"status"`
}

func (s *server) knowledgeDTO(ctx context.Context, q *store.Queries, k store.KnowledgeSource) knowledgeSourceDTO {
	tok := apiKeyStatus{Source: "none"}
	switch {
	case k.TokenSecretID != nil:
		tok = apiKeyStatus{Source: "secret", SecretID: k.TokenSecretID}
		if sec, err := q.GetSecretAnyScope(ctx, store.GetSecretAnyScopeParams{ID: *k.TokenSecretID, WorkspaceID: k.WorkspaceID}); err == nil {
			tok.SecretName, tok.Last4 = sec.Name, sec.Last4
		}
	case k.TokenEnv != nil && *k.TokenEnv != "":
		tok = apiKeyStatus{Source: "env", Env: *k.TokenEnv, EnvSet: s.env(*k.TokenEnv) != ""}
	}
	ex := k.Exclude
	if ex == nil {
		ex = []string{}
	}
	return knowledgeSourceDTO{k.ID, k.Name, k.Repo, k.Branch, k.PathPrefix, ex, tok, k.Enabled, k.LastCommit, k.LastSyncedAt, k.LastError, k.Status}
}

func (s *server) handleGetKnowledge(c *gin.Context) {
	ws := memberOf(c).WorkspaceID
	ctx := c.Request.Context()
	q := store.New(s.pool)
	rows, err := q.ListKnowledgeSources(ctx, ws)
	if err != nil {
		s.fail(c, err)
		return
	}
	sources := make([]knowledgeSourceDTO, 0, len(rows))
	for _, r := range rows {
		sources = append(sources, s.knowledgeDTO(ctx, q, r))
	}
	model := ""
	if e, err := s.router.Embedder(ctx, ws); err == nil {
		model = e.Model()
	}
	counts, err := q.KnowledgeCounts(ctx, store.KnowledgeCountsParams{WorkspaceID: ws, Model: model})
	if err != nil {
		s.fail(c, err)
		return
	}
	files, err := q.ListMemoryBank(ctx, ws)
	if err != nil {
		s.fail(c, err)
		return
	}
	type fileDTO struct {
		Name      string    `json:"name"`
		UpdatedAt time.Time `json:"updated_at"`
		CommitSha *string   `json:"commit_sha"`
		Chars     int       `json:"chars"`
	}
	mb := make([]fileDTO, 0, len(files))
	for _, f := range files {
		mb = append(mb, fileDTO{f.Name, f.UpdatedAt, f.CommitSha, len(f.Content)})
	}
	c.JSON(http.StatusOK, gin.H{"sources": sources, "counts": counts, "memory_bank": mb, "core_files": knowledge.CoreFileNames(), "default_exclude": knowledge.DefaultExclude})
}

type knowledgeInput struct {
	Name       string   `json:"name"`
	Repo       string   `json:"repo"`
	Branch     string   `json:"branch"`
	PathPrefix string   `json:"path_prefix"`
	Exclude    []string `json:"exclude"`
	Token      *string  `json:"token"`     // paste: stored as an encrypted workspace secret
	TokenEnv   string   `json:"token_env"` // or an env var name
	ClearToken bool     `json:"clear_token"`
	Enabled    *bool    `json:"enabled"`
}

func (in *knowledgeInput) validate() error {
	in.Repo = strings.TrimSpace(in.Repo)
	if !repoRe.MatchString(in.Repo) {
		return badRequest("repo must be owner/name")
	}
	if in.Branch = strings.TrimSpace(in.Branch); in.Branch == "" {
		in.Branch = "main"
	}
	if len(in.Branch) > 100 || strings.ContainsAny(in.Branch, " ~^:?*[\\") {
		return badRequest("branch is not a valid branch name")
	}
	in.PathPrefix = strings.Trim(strings.TrimSpace(in.PathPrefix), "/")
	if strings.Contains(in.PathPrefix, "..") || len(in.PathPrefix) > 200 {
		return badRequest("path_prefix must be a directory inside the repository")
	}
	if in.PathPrefix != "" {
		in.PathPrefix += "/"
	}
	if in.Name = strings.TrimSpace(in.Name); in.Name == "" {
		in.Name = in.Repo
	}
	if len(in.Name) > 100 {
		return badRequest("name is limited to 100 characters")
	}
	if in.Exclude == nil {
		in.Exclude = slices.Clone(knowledge.DefaultExclude)
	}
	if len(in.Exclude) > 30 {
		return badRequest("at most 30 exclude patterns")
	}
	clean := []string{}
	for _, e := range in.Exclude {
		if e = strings.TrimSpace(e); e != "" && len(e) <= 200 {
			clean = append(clean, e)
		}
	}
	in.Exclude = clean
	if in.TokenEnv != "" && !envNameRe.MatchString(in.TokenEnv) {
		return badRequest("token_env must be an environment variable name")
	}
	return nil
}

func (s *server) handleSaveKnowledge(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	creating := c.Request.Method == http.MethodPost
	var id uuid.UUID
	if !creating {
		var err error
		if id, err = pathUUID(c, "id"); err != nil {
			s.fail(c, err)
			return
		}
	}
	var in knowledgeInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := in.validate(); err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	var saved store.KnowledgeSource
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		var existing *store.KnowledgeSource
		if !creating {
			cur, err := q.GetKnowledgeSource(ctx, store.GetKnowledgeSourceParams{WorkspaceID: m.WorkspaceID, ID: id})
			if err != nil {
				return err
			}
			existing = &cur
		}
		var secretID *uuid.UUID
		if existing != nil && !in.ClearToken {
			secretID = existing.TokenSecretID
		}
		if in.Token != nil && strings.TrimSpace(*in.Token) != "" {
			sid, err := s.createSecret(ctx, q, fmt.Sprintf("%s read token (%s)", in.Repo, time.Now().UTC().Format("2006-01-02 15:04:05")), *in.Token, &m.WorkspaceID, u.ID)
			if err != nil {
				return err
			}
			secretID = &sid
		}
		var tokenEnv *string
		if in.TokenEnv != "" && secretID == nil {
			tokenEnv = &in.TokenEnv
		}
		enabled := in.Enabled == nil || *in.Enabled
		var err error
		if creating {
			saved, err = q.CreateKnowledgeSource(ctx, store.CreateKnowledgeSourceParams{WorkspaceID: m.WorkspaceID, Name: in.Name, Repo: in.Repo, Branch: in.Branch,
				PathPrefix: in.PathPrefix, Exclude: in.Exclude, TokenSecretID: secretID, TokenEnv: tokenEnv, Enabled: enabled})
		} else {
			saved, err = q.UpdateKnowledgeSource(ctx, store.UpdateKnowledgeSourceParams{WorkspaceID: m.WorkspaceID, ID: id, Name: in.Name, Repo: in.Repo, Branch: in.Branch,
				PathPrefix: in.PathPrefix, Exclude: in.Exclude, TokenSecretID: secretID, TokenEnv: tokenEnv, Enabled: enabled})
		}
		if err != nil {
			return err
		}
		// Sync the new settings unless a sync or build is already running
		// (Sync now picks it up afterwards).
		if busy, err := jobs.KnowledgeBusy(ctx, s.jobs.JobList, m.WorkspaceID); err == nil && !busy {
			if _, err := s.jobs.InsertTx(ctx, tx, jobs.SyncKnowledgeArgs{WorkspaceID: m.WorkspaceID, SourceID: saved.ID}, nil); err != nil {
				return err
			}
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "knowledge.save", "knowledge_source", saved.ID.String(), map[string]any{"repo": in.Repo, "branch": in.Branch, "token_changed": in.Token != nil || in.ClearToken})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	c.JSON(status, s.knowledgeDTO(ctx, store.New(s.pool), saved))
}

func (s *server) handleDeleteKnowledge(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		n, err := q.DeleteKnowledgeSource(ctx, store.DeleteKnowledgeSourceParams{WorkspaceID: m.WorkspaceID, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotFound
		}
		if err := q.DeleteOrphanKnowledgeEmbeddings(ctx, m.WorkspaceID); err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "knowledge.delete", "knowledge_source", id.String(), nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *server) handleSyncKnowledge(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	force := c.Query("force") == "true"
	ctx := c.Request.Context()
	if err := s.knowledgeIdle(ctx, m.WorkspaceID); err != nil {
		s.fail(c, err)
		return
	}
	err = s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		if _, err := q.GetKnowledgeSource(ctx, store.GetKnowledgeSourceParams{WorkspaceID: m.WorkspaceID, ID: id}); err != nil {
			return err
		}
		if _, err := s.jobs.InsertTx(ctx, tx, jobs.SyncKnowledgeArgs{WorkspaceID: m.WorkspaceID, SourceID: id, Force: force}, nil); err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "knowledge.sync", "knowledge_source", id.String(), map[string]any{"force": force})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"queued": true})
}

func (s *server) handleRebuildMemoryBank(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	ctx := c.Request.Context()
	if err := s.knowledgeIdle(ctx, m.WorkspaceID); err != nil {
		s.fail(c, err)
		return
	}
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		if _, err := s.jobs.InsertTx(ctx, tx, jobs.BuildMemoryBankArgs{WorkspaceID: m.WorkspaceID}, nil); err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "knowledge.rebuild", "workspace", m.WorkspaceID.String(), nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"queued": true})
}

// knowledgeIdle refuses to start a sync or build while one is queued or
// running for the workspace: they would compete for the same docs.
func (s *server) knowledgeIdle(ctx context.Context, ws uuid.UUID) error {
	busy, err := jobs.KnowledgeBusy(ctx, s.jobs.JobList, ws)
	if err != nil {
		return err
	}
	if busy {
		return &apiError{http.StatusConflict, "busy", "A sync or memory bank build is already in progress. Try again when it finishes."}
	}
	return nil
}

func (s *server) handleKnowledgeDocs(c *gin.Context) {
	ws := memberOf(c).WorkspaceID
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	rows, err := store.New(s.pool).ListKnowledgeDocs(c.Request.Context(), store.ListKnowledgeDocsParams{WorkspaceID: ws, SourceID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	type docDTO struct {
		Path      string    `json:"path"`
		Title     string    `json:"title"`
		Chunks    int32     `json:"chunks"`
		Digested  bool      `json:"digested"`
		Informs   []string  `json:"informs"`
		UpdatedAt time.Time `json:"updated_at"`
		URL       string    `json:"url"`
	}
	out := make([]docDTO, 0, len(rows))
	src, _ := store.New(s.pool).GetKnowledgeSource(c.Request.Context(), store.GetKnowledgeSourceParams{WorkspaceID: ws, ID: id})
	for _, r := range rows {
		informs := r.Informs
		if informs == nil {
			informs = []string{}
		}
		out = append(out, docDTO{r.Path, r.Title, r.Chunks, r.Digested != nil && *r.Digested, informs, r.UpdatedAt, knowledge.BlobURL(src.Repo, r.CommitSha, r.Path, "")})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// handleMemoryBank returns the memory bank files (workspace members). They
// describe the project's internals and stay inside the app.
func (s *server) handleMemoryBank(c *gin.Context) {
	files, err := store.New(s.pool).ListMemoryBank(c.Request.Context(), memberOf(c).WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	type fileDTO struct {
		Name      string    `json:"name"`
		Content   string    `json:"content"`
		CommitSha *string   `json:"commit_sha"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	order := map[string]int{}
	for i, n := range knowledge.CoreFileNames() {
		order[n] = i
	}
	slices.SortFunc(files, func(a, b store.MemoryBank) int { return order[a.Name] - order[b.Name] })
	out := make([]fileDTO, 0, len(files))
	for _, f := range files {
		out = append(out, fileDTO{f.Name, f.Content, f.CommitSha, f.UpdatedAt})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}
