package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/config"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/research"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/search"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

var (
	envNameRe    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	providerIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	efforts      = []string{"low", "medium", "high", "xhigh", "max"}
)

// requiredCaps lists capabilities a model must declare to serve a task.
var requiredCaps = map[string]func(llm.Capabilities) error{
	"research": func(c llm.Capabilities) error {
		if !c.Tools {
			return errors.New("research requires a model that declares tool support")
		}
		return nil
	},
	"classify": needSchema, "score": needSchema,
}

func needSchema(c llm.Capabilities) error {
	if !c.JSONSchema {
		return errors.New("this task requires a model that declares structured output support")
	}
	return nil
}

// ---- secrets ----

type secretDTO struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	Scope           string    `json:"scope"` // instance | workspace
	Last4           string    `json:"last4"`
	UpdatedAt       time.Time `json:"updated_at"`
	UsedByProviders []string  `json:"used_by_providers"`
}

func (s *server) handleListSecrets(c *gin.Context) {
	rows, err := store.New(s.pool).ListSecrets(c.Request.Context(), ptrUUID(memberOf(c).WorkspaceID))
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]secretDTO, 0, len(rows))
	for _, r := range rows {
		scope := "instance"
		if r.WorkspaceID != nil {
			scope = "workspace"
		}
		out = append(out, secretDTO{r.ID, r.Name, scope, r.Last4, r.UpdatedAt, r.UsedByProviders})
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "encryption_enabled": s.box.Enabled()})
}

type secretInput struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
	Scope string  `json:"scope"` // create only: instance (default) | workspace
}

func (s *server) scopeFor(c *gin.Context, scope string) (*uuid.UUID, error) {
	switch scope {
	case "", "instance":
		return nil, nil
	case "workspace":
		return ptrUUID(memberOf(c).WorkspaceID), nil
	}
	return nil, badRequest("scope must be instance or workspace")
}

// createSecret seals value under a new id. The value is never logged.
func (s *server) createSecret(ctx context.Context, q *store.Queries, name, value string, scope *uuid.UUID, by uuid.UUID) (uuid.UUID, error) {
	if !s.box.Enabled() {
		return uuid.Nil, &apiError{http.StatusConflict, "encryption_disabled", secrets.ErrNoKey.Error()}
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 8192 {
		return uuid.Nil, badRequest("value must be 1-8192 characters")
	}
	id := uuid.New()
	sealed, err := s.box.Seal(value, id, scope)
	if err != nil {
		return uuid.Nil, err
	}
	_, err = q.CreateSecret(ctx, store.CreateSecretParams{ID: id, WorkspaceID: scope, Name: name, Ciphertext: sealed.Ciphertext,
		Nonce: sealed.Nonce, KeyID: sealed.KeyID, Last4: secrets.Last4(value), CreatedBy: &by})
	return id, err
}

func (s *server) handleCreateSecret(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	var in secretInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := reqString("name", &in.Name, 100); err != nil {
		s.fail(c, err)
		return
	}
	if in.Value == nil {
		s.fail(c, badRequest("value is required"))
		return
	}
	scope, err := s.scopeFor(c, in.Scope)
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	var id uuid.UUID
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		var err error
		if id, err = s.createSecret(ctx, q, in.Name, *in.Value, scope, u.ID); err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "secret.create", "secret", id.String(), map[string]any{"name": in.Name})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// secretScope finds which scope (instance or this workspace) holds id.
func (s *server) secretScope(ctx context.Context, q *store.Queries, ws, id uuid.UUID) (*uuid.UUID, error) {
	if _, err := q.GetSecret(ctx, store.GetSecretParams{ID: id}); err == nil {
		return nil, nil
	}
	if _, err := q.GetSecret(ctx, store.GetSecretParams{ID: id, WorkspaceID: &ws}); err != nil {
		return nil, errNotFound
	}
	return &ws, nil
}

func (s *server) handleUpdateSecret(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	var in secretInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		scope, err := s.secretScope(ctx, q, m.WorkspaceID, id)
		if err != nil {
			return err
		}
		if strings.TrimSpace(in.Name) != "" {
			if err := reqString("name", &in.Name, 100); err != nil {
				return err
			}
			if _, err := q.RenameSecret(ctx, store.RenameSecretParams{ID: id, Name: in.Name, WorkspaceID: scope}); err != nil {
				return err
			}
		}
		if in.Value != nil {
			if !s.box.Enabled() {
				return &apiError{http.StatusConflict, "encryption_disabled", secrets.ErrNoKey.Error()}
			}
			v := strings.TrimSpace(*in.Value)
			if v == "" || len(v) > 8192 {
				return badRequest("value must be 1-8192 characters")
			}
			sealed, err := s.box.Seal(v, id, scope)
			if err != nil {
				return err
			}
			if _, err := q.UpdateSecretValue(ctx, store.UpdateSecretValueParams{ID: id, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce,
				KeyID: sealed.KeyID, Last4: secrets.Last4(v), WorkspaceID: scope}); err != nil {
				return err
			}
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "secret.update", "secret", id.String(), map[string]any{"value_changed": in.Value != nil})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *server) handleDeleteSecret(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		scope, err := s.secretScope(ctx, q, m.WorkspaceID, id)
		if err != nil {
			return err
		}
		if _, err := q.DeleteSecret(ctx, store.DeleteSecretParams{ID: id, WorkspaceID: scope}); err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "secret.delete", "secret", id.String(), nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- providers ----

type apiKeyStatus struct {
	Source     string     `json:"source"` // secret | env | none
	SecretID   *uuid.UUID `json:"secret_id"`
	SecretName string     `json:"secret_name,omitempty"`
	Last4      string     `json:"last4,omitempty"`
	Env        string     `json:"env,omitempty"`
	EnvSet     bool       `json:"env_set"`
}

type providerDTO struct {
	ID      string                 `json:"id"`
	Name    string                 `json:"name"`
	Kind    string                 `json:"kind"`
	BaseURL string                 `json:"base_url"`
	IsLocal bool                   `json:"is_local"`
	Enabled bool                   `json:"enabled"`
	APIKey  apiKeyStatus           `json:"api_key"`
	Headers map[string]string      `json:"headers"`
	Options router.ProviderOptions `json:"options"`
}

func (s *server) toProviderDTO(ctx context.Context, q *store.Queries, p store.ModelProvider) providerDTO {
	d := providerDTO{ID: p.ID, Kind: p.Kind, IsLocal: p.IsLocal, Enabled: p.Enabled, Headers: map[string]string{}}
	if p.Name != nil {
		d.Name = *p.Name
	}
	if p.BaseUrl != nil {
		d.BaseURL = *p.BaseUrl
	}
	_ = json.Unmarshal(orEmptyJSON(p.Headers), &d.Headers)
	_ = json.Unmarshal(orEmptyJSON(p.Options), &d.Options)
	k := apiKeyStatus{Source: "none"}
	if p.ApiKeyEnv != nil && *p.ApiKeyEnv != "" {
		k.Source, k.Env, k.EnvSet = "env", *p.ApiKeyEnv, s.env(*p.ApiKeyEnv) != ""
	}
	if p.ApiKeySecretID != nil {
		k.Source, k.SecretID = "secret", p.ApiKeySecretID
		if sec, err := q.GetSecret(ctx, store.GetSecretParams{ID: *p.ApiKeySecretID}); err == nil {
			k.SecretName, k.Last4 = sec.Name, sec.Last4
		}
	}
	d.APIKey = k
	return d
}

func orEmptyJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}

func (s *server) handleListProviders(c *gin.Context) {
	ctx := c.Request.Context()
	q := store.New(s.pool)
	rows, err := q.ListProviders(ctx)
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]providerDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.toProviderDTO(ctx, q, r))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "encryption_enabled": s.box.Enabled()})
}

type providerInput struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Kind     string                 `json:"kind"`
	BaseURL  string                 `json:"base_url"`
	IsLocal  bool                   `json:"is_local"`
	Enabled  *bool                  `json:"enabled"`
	APIKey   *string                `json:"api_key"`           // paste a key: stored as an encrypted instance secret
	SecretID *uuid.UUID             `json:"api_key_secret_id"` // or reference an existing secret
	KeyEnv   string                 `json:"api_key_env"`       // or an env var name
	ClearKey bool                   `json:"clear_api_key"`
	Headers  map[string]string      `json:"headers"`
	Options  router.ProviderOptions `json:"options"`
}

func (in *providerInput) validate(creating bool) error {
	if creating && !providerIDRe.MatchString(in.ID) {
		return badRequest("id must be 1-40 lowercase letters, digits, - or _")
	}
	if err := oneOf("kind", in.Kind, config.KindAnthropic, config.KindOpenAICompat); err != nil {
		return err
	}
	if err := optString("name", &in.Name, 100); err != nil {
		return err
	}
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	if in.BaseURL != "" {
		if _, err := optURL("base_url", &in.BaseURL); err != nil {
			return err
		}
	}
	if in.Kind == config.KindOpenAICompat && in.BaseURL == "" {
		return badRequest("base_url is required for openai_compat providers")
	}
	if in.Kind == config.KindAnthropic && in.IsLocal {
		return badRequest("anthropic providers cannot be local")
	}
	in.KeyEnv = strings.TrimSpace(in.KeyEnv)
	if in.KeyEnv != "" && !envNameRe.MatchString(in.KeyEnv) {
		return badRequest("api_key_env must be an env var name like ANTHROPIC_API_KEY")
	}
	for k := range in.Headers {
		if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "x-api-key") {
			return badRequest("put credentials in the API key field, not in headers")
		}
	}
	if s := in.Options.ReasoningStyle; s != "" {
		if err := oneOf("options.reasoning_style", s, "reasoning_effort", "chat_template"); err != nil {
			return err
		}
	}
	if p := in.Options.MaxTokensParam; p != "" {
		if err := oneOf("options.max_tokens_param", p, "max_tokens", "max_completion_tokens"); err != nil {
			return err
		}
	}
	if in.Options.MaxRetries < -1 || in.Options.MaxRetries > 10 {
		return badRequest("options.max_retries must be between -1 and 10")
	}
	return nil
}

func (s *server) handleSaveProvider(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	creating := c.Request.Method == http.MethodPost
	var in providerInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if !creating {
		in.ID = c.Param("id")
	}
	if err := in.validate(creating); err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	var saved store.ModelProvider
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		var existing *store.ModelProvider
		if !creating {
			row, err := q.GetProvider(ctx, in.ID)
			if err != nil {
				return err
			}
			existing = &row
		}
		secretID := in.SecretID
		if secretID == nil && existing != nil && !in.ClearKey {
			secretID = existing.ApiKeySecretID
		}
		if in.ClearKey {
			secretID = nil
		}
		if in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "" {
			label := in.Name
			if label == "" {
				label = in.ID
			}
			id, err := s.createSecret(ctx, q, fmt.Sprintf("%s API key (%s)", label, time.Now().UTC().Format("2006-01-02 15:04:05")), *in.APIKey, nil, u.ID)
			if err != nil {
				return err
			}
			secretID = &id
		} else if in.SecretID != nil {
			if _, err := q.GetSecret(ctx, store.GetSecretParams{ID: *in.SecretID}); err != nil {
				return badRequest("api_key_secret_id must reference an instance-level secret")
			}
		}
		headers, _ := json.Marshal(nonNilMap(in.Headers))
		opts, _ := json.Marshal(in.Options)
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		var base, name, keyEnv *string
		if in.BaseURL != "" {
			base = &in.BaseURL
		}
		if in.Name != "" {
			name = &in.Name
		}
		if in.KeyEnv != "" {
			keyEnv = &in.KeyEnv
		}
		var err error
		if creating {
			saved, err = q.CreateProvider(ctx, store.CreateProviderParams{ID: in.ID, Kind: in.Kind, BaseUrl: base, IsLocal: in.IsLocal, Name: name,
				ApiKeySecretID: secretID, ApiKeyEnv: keyEnv, Headers: headers, Options: opts, Enabled: enabled})
		} else {
			saved, err = q.UpdateProvider(ctx, store.UpdateProviderParams{ID: in.ID, Kind: in.Kind, BaseUrl: base, IsLocal: in.IsLocal, Name: name,
				ApiKeySecretID: secretID, ApiKeyEnv: keyEnv, Headers: headers, Options: opts, Enabled: enabled})
		}
		if err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "provider.save", "model_provider", in.ID,
			map[string]any{"kind": in.Kind, "local": in.IsLocal, "key_changed": in.APIKey != nil || in.SecretID != nil || in.ClearKey})
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
	c.JSON(status, s.toProviderDTO(ctx, store.New(s.pool), saved))
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (s *server) handleDeleteProvider(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id := c.Param("id")
	ctx := c.Request.Context()
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		n, err := q.DeleteProvider(ctx, id)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return &apiError{http.StatusConflict, "in_use", "a task route still uses this provider; change the route first"}
			}
			return err
		}
		if n == 0 {
			return errNotFound
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "provider.delete", "model_provider", id, nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *server) handleTestProvider(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	t, err := s.router.Target(ctx, c.Param("id"), "")
	if err != nil {
		s.fail(c, err)
		return
	}
	start := time.Now()
	models, err := t.Provider.ListModels(ctx)
	res := gin.H{"ok": err == nil, "latency_ms": time.Since(start).Milliseconds(), "models": len(models)}
	if err != nil {
		res["error"] = err.Error()
	}
	c.JSON(http.StatusOK, res)
}

func (s *server) handleDiscoverModels(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	t, err := s.router.Target(ctx, c.Param("id"), "")
	if err != nil {
		s.fail(c, err)
		return
	}
	models, err := t.Provider.ListModels(ctx)
	if err != nil {
		s.fail(c, &apiError{http.StatusBadGateway, "provider_error", err.Error()})
		return
	}
	if models == nil {
		models = []llm.ModelInfo{}
	}
	c.JSON(http.StatusOK, gin.H{"items": models})
}

// handleProbeEmbeddings embeds a short text with the chosen model and
// reports the vector length, to fill in the dimensions field.
func (s *server) handleProbeEmbeddings(c *gin.Context) {
	var in struct {
		ProviderID string `json:"provider_id"`
		Model      string `json:"model"`
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if in.ProviderID == "" || strings.TrimSpace(in.Model) == "" {
		s.fail(c, badRequest("choose a provider and a model"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	dims, err := s.router.ProbeEmbedding(ctx, memberOf(c).WorkspaceID, in.ProviderID, strings.TrimSpace(in.Model))
	if err != nil {
		s.fail(c, &apiError{http.StatusBadGateway, "provider_error", err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"dimensions": dims})
}

// ---- models ----

type modelDTO struct {
	ProviderID    string           `json:"provider_id"`
	Model         string           `json:"model"`
	DisplayName   string           `json:"display_name"`
	Capabilities  llm.Capabilities `json:"capabilities"`
	DefaultParams router.Params    `json:"default_params"`
	Price         router.Price     `json:"price"`
	Enabled       bool             `json:"enabled"`
}

func toModelDTO(m store.ProviderModel) modelDTO {
	d := modelDTO{ProviderID: m.ProviderID, Model: m.Model, Enabled: m.Enabled}
	if m.DisplayName != nil {
		d.DisplayName = *m.DisplayName
	}
	_ = json.Unmarshal(orEmptyJSON(m.Capabilities), &d.Capabilities)
	d.DefaultParams, _ = router.ParseParams(m.DefaultParams)
	_ = json.Unmarshal(orEmptyJSON(m.Price), &d.Price)
	return d
}

func (s *server) handleListModels(c *gin.Context) {
	rows, err := store.New(s.pool).ListProviderModels(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]modelDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toModelDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// validateParams checks params against what the model declares.
func validateParams(p router.Params, caps llm.Capabilities) error {
	if (p.Temperature != nil || p.TopP != nil) && !caps.Sampling {
		return badRequest("this model does not accept temperature/top_p (declare sampling support first)")
	}
	if p.Temperature != nil && (*p.Temperature < 0 || *p.Temperature > 2) {
		return badRequest("temperature must be between 0 and 2")
	}
	if p.TopP != nil && (*p.TopP <= 0 || *p.TopP > 1) {
		return badRequest("top_p must be in (0, 1]")
	}
	if p.MaxTokens < 0 || (caps.MaxOutput > 0 && p.MaxTokens > caps.MaxOutput) {
		return badRequest(fmt.Sprintf("max_tokens must be between 0 and %d", caps.MaxOutput))
	}
	if t := p.Thinking; t != nil {
		switch {
		case caps.Thinking == "":
			return badRequest("this model does not declare thinking support")
		case caps.Thinking == "effort" && t.BudgetTokens > 0:
			return badRequest("this model takes thinking effort, not a token budget")
		case caps.Thinking == "budget" && t.Effort != "":
			return badRequest("this model takes a thinking token budget, not effort")
		case t.Effort != "" && !slices.Contains(efforts, t.Effort):
			return badRequest("thinking.effort must be one of: " + strings.Join(efforts, ", "))
		case caps.Thinking == "budget" && t.BudgetTokens > 0 && t.BudgetTokens < 1024:
			return badRequest("thinking.budget_tokens must be at least 1024")
		}
	}
	if p.MaxSteps < 0 || p.MaxSteps > 100 || p.TimeoutSec < 0 || p.TimeoutSec > 3600 || p.TokenBudget < 0 {
		return badRequest("agent limits out of range (max_steps 0-100, timeout_s 0-3600)")
	}
	return nil
}

func (s *server) handleSaveModel(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	var in modelDTO
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := reqString("model", &in.Model, 200); err != nil {
		s.fail(c, err)
		return
	}
	if err := validateParams(in.DefaultParams, in.Capabilities); err != nil {
		s.fail(c, err)
		return
	}
	if in.Price.InputPerMTok < 0 || in.Price.OutputPerMTok < 0 {
		s.fail(c, badRequest("prices cannot be negative"))
		return
	}
	caps, _ := json.Marshal(in.Capabilities)
	params, _ := json.Marshal(in.DefaultParams)
	price, _ := json.Marshal(in.Price)
	var name *string
	if strings.TrimSpace(in.DisplayName) != "" {
		name = &in.DisplayName
	}
	ctx := c.Request.Context()
	var saved store.ProviderModel
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		if _, err := q.GetProvider(ctx, in.ProviderID); err != nil {
			return badRequest("unknown provider_id")
		}
		var err error
		saved, err = q.UpsertProviderModel(ctx, store.UpsertProviderModelParams{ProviderID: in.ProviderID, Model: in.Model, DisplayName: name,
			Capabilities: caps, DefaultParams: params, Price: price, Enabled: in.Enabled})
		if err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "model.save", "provider_model", in.ProviderID+"/"+in.Model, nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toModelDTO(saved))
}

func (s *server) handleDeleteModel(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	provider, model := c.Query("provider_id"), c.Query("model")
	ctx := c.Request.Context()
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		n, err := q.DeleteProviderModel(ctx, store.DeleteProviderModelParams{ProviderID: provider, Model: model})
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotFound
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "model.delete", "provider_model", provider+"/"+model, nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- routes ----

type routeDTO struct {
	Task               string        `json:"task"`
	ProviderID         string        `json:"provider_id"`
	Model              string        `json:"model"`
	FallbackProviderID *string       `json:"fallback_provider_id"`
	FallbackModel      *string       `json:"fallback_model"`
	Params             router.Params `json:"params"`
	Tools              []string      `json:"tools"`
	MCPServerIDs       []uuid.UUID   `json:"mcp_server_ids"`
	Configured         bool          `json:"configured"`
	// Status: the effective routing decision right now.
	Usable    bool     `json:"usable"`
	Effective []string `json:"effective"` // provider/model in try order
	Problems  []string `json:"problems"`
}

func (s *server) handleListRoutes(c *gin.Context) {
	ctx := c.Request.Context()
	ws := memberOf(c).WorkspaceID
	q := store.New(s.pool)
	out := make([]routeDTO, 0, len(config.KnownTasks))
	for _, task := range config.KnownTasks {
		d := routeDTO{Task: task, Tools: []string{}, MCPServerIDs: []uuid.UUID{}, Effective: []string{}, Problems: []string{}}
		row, err := q.GetTaskRoute(ctx, store.GetTaskRouteParams{WorkspaceID: ws, Task: task})
		if err == nil {
			d.Configured = true
			d.ProviderID, d.Model = row.ProviderID, row.Model
			d.FallbackProviderID, d.FallbackModel = row.FallbackProviderID, row.FallbackModel
			d.Params, _ = router.ParseParams(row.Params)
			d.Tools, d.MCPServerIDs = row.Tools, row.McpServerIds
		} else if !errors.Is(err, pgx.ErrNoRows) {
			s.fail(c, err)
			return
		}
		res, rerr := s.router.Resolve(ctx, ws, task)
		if res != nil {
			for _, t := range res.Targets {
				d.Effective = append(d.Effective, t.ProviderID+"/"+t.Model)
			}
			d.Problems = append(d.Problems, res.Skipped...)
		}
		d.Usable = rerr == nil
		if rerr != nil && res == nil {
			d.Problems = append(d.Problems, rerr.Error())
		}
		out = append(out, d)
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "builtin_tools": research.BuiltinTools})
}

// capsFor returns declared capabilities for provider/model (zero if undeclared).
func capsFor(ctx context.Context, q *store.Queries, provider, model string) (llm.Capabilities, error) {
	var caps llm.Capabilities
	pm, err := q.GetProviderModel(ctx, store.GetProviderModelParams{ProviderID: provider, Model: model})
	if errors.Is(err, pgx.ErrNoRows) {
		return caps, badRequest(fmt.Sprintf("declare %s/%s under Models before routing to it", provider, model))
	}
	if err != nil {
		return caps, err
	}
	err = json.Unmarshal(orEmptyJSON(pm.Capabilities), &caps)
	return caps, err
}

func (s *server) handleSaveRoute(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	task := c.Param("task")
	if !slices.Contains(config.KnownTasks, task) {
		s.fail(c, errNotFound)
		return
	}
	var in routeDTO
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if (in.FallbackProviderID == nil) != (in.FallbackModel == nil) {
		s.fail(c, badRequest("set both fallback_provider_id and fallback_model, or neither"))
		return
	}
	if in.FallbackProviderID != nil && *in.FallbackProviderID == in.ProviderID && *in.FallbackModel == in.Model {
		s.fail(c, badRequest("the fallback is identical to the primary"))
		return
	}
	ctx := c.Request.Context()
	var saved store.TaskRoute
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		check := func(provider, model string) error {
			caps, err := capsFor(ctx, q, provider, model)
			if err != nil {
				return err
			}
			if req := requiredCaps[task]; req != nil {
				if err := req(caps); err != nil {
					return badRequest(fmt.Sprintf("%s/%s: %v", provider, model, err))
				}
			}
			if err := validateParams(in.Params, caps); err != nil {
				return badRequest(fmt.Sprintf("%s/%s: %s", provider, model, err.Error()))
			}
			if len(in.MCPServerIDs) > 0 && !caps.Tools {
				return badRequest(fmt.Sprintf("%s/%s does not declare tool support, so it cannot use MCP servers", provider, model))
			}
			return nil
		}
		if err := check(in.ProviderID, in.Model); err != nil {
			return err
		}
		if in.FallbackProviderID != nil {
			if err := check(*in.FallbackProviderID, *in.FallbackModel); err != nil {
				return err
			}
		}
		if len(in.MCPServerIDs) > 0 {
			servers, err := q.GetMCPServersByIDs(ctx, store.GetMCPServersByIDsParams{WorkspaceID: m.WorkspaceID, Ids: in.MCPServerIDs})
			if err != nil {
				return err
			}
			if len(servers) != len(in.MCPServerIDs) {
				return badRequest("mcp_server_ids must reference enabled servers in this workspace")
			}
		}
		params, _ := json.Marshal(in.Params)
		tools := in.Tools
		if tools == nil {
			tools = []string{}
		}
		mcpIDs := in.MCPServerIDs
		if mcpIDs == nil {
			mcpIDs = []uuid.UUID{}
		}
		var err error
		saved, err = q.UpsertTaskRoute(ctx, store.UpsertTaskRouteParams{WorkspaceID: m.WorkspaceID, Task: task, ProviderID: in.ProviderID, Model: in.Model,
			FallbackProviderID: in.FallbackProviderID, FallbackModel: in.FallbackModel, Params: params, Tools: tools, McpServerIds: mcpIDs})
		if err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "route.save", "task_route", task,
			map[string]any{"provider": in.ProviderID, "model": in.Model, "fallback": in.FallbackProviderID})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"task": saved.Task, "provider_id": saved.ProviderID, "model": saved.Model})
}

// ---- prompts ----

type promptDTO struct {
	ID        uuid.UUID `json:"id"`
	Task      string    `json:"task"`
	Version   int32     `json:"version"`
	Body      string    `json:"body"`
	Notes     *string   `json:"notes"`
	Active    bool      `json:"active"`
	Scope     string    `json:"scope"` // default | workspace
	CreatedAt time.Time `json:"created_at"`
}

func toPromptDTO(p store.Prompt) promptDTO {
	scope := "default"
	if p.WorkspaceID != nil {
		scope = "workspace"
	}
	return promptDTO{p.ID, p.Task, p.Version, p.Body, p.Notes, p.Active, scope, p.CreatedAt}
}

func (s *server) handleListPrompts(c *gin.Context) {
	task := c.Query("task")
	if !slices.Contains(config.PromptTasks, task) {
		s.fail(c, badRequest("task must be one of: "+strings.Join(config.PromptTasks, ", ")))
		return
	}
	ctx := c.Request.Context()
	ws := memberOf(c).WorkspaceID
	q := store.New(s.pool)
	rows, err := q.ListPrompts(ctx, store.ListPromptsParams{WorkspaceID: &ws, Task: task})
	if err != nil {
		s.fail(c, err)
		return
	}
	// Exactly one version is effective: the workspace's active one, else the default's.
	var effective *uuid.UUID
	if p, err := q.GetActivePrompt(ctx, store.GetActivePromptParams{WorkspaceID: &ws, Task: task}); err == nil {
		effective = &p.ID
	}
	out := make([]promptDTO, 0, len(rows))
	for _, r := range rows {
		d := toPromptDTO(r)
		d.Active = effective != nil && *effective == r.ID
		out = append(out, d)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (s *server) handleCreatePrompt(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	var in struct {
		Task     string  `json:"task"`
		Body     string  `json:"body"`
		Notes    *string `json:"notes"`
		Activate bool    `json:"activate"`
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if !slices.Contains(config.PromptTasks, in.Task) {
		s.fail(c, badRequest("unknown task"))
		return
	}
	if err := reqString("body", &in.Body, 50_000); err != nil {
		s.fail(c, err)
		return
	}
	if err := optString("notes", in.Notes, 1000); err != nil {
		s.fail(c, err)
		return
	}
	if _, err := prompts.Render(in.Task, in.Body, prompts.SampleData()); err != nil {
		s.fail(c, badRequest("prompt does not render: "+err.Error()+" (available fields: "+prompts.Fields+")"))
		return
	}
	ctx := c.Request.Context()
	var saved store.Prompt
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		var err error
		saved, err = q.CreatePromptVersion(ctx, store.CreatePromptVersionParams{WorkspaceID: &m.WorkspaceID, Task: in.Task, Body: in.Body, Notes: in.Notes, CreatedBy: &u.ID})
		if err != nil {
			return err
		}
		if in.Activate {
			if err := q.DeactivateWorkspacePrompts(ctx, store.DeactivateWorkspacePromptsParams{WorkspaceID: &m.WorkspaceID, Task: in.Task}); err != nil {
				return err
			}
			if _, err := q.ActivateWorkspacePrompt(ctx, store.ActivateWorkspacePromptParams{ID: saved.ID, WorkspaceID: &m.WorkspaceID}); err != nil {
				return err
			}
			saved.Active = true
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "prompt.create", "prompt", saved.ID.String(), map[string]any{"task": in.Task, "version": saved.Version, "activated": in.Activate})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, toPromptDTO(saved))
}

// handleActivatePrompt makes a version effective. Activating a shipped
// default means deactivating the workspace's own versions.
func (s *server) handleActivatePrompt(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		p, err := q.GetPrompt(ctx, store.GetPromptParams{ID: id, WorkspaceID: &m.WorkspaceID})
		if err != nil {
			return err
		}
		if err := q.DeactivateWorkspacePrompts(ctx, store.DeactivateWorkspacePromptsParams{WorkspaceID: &m.WorkspaceID, Task: p.Task}); err != nil {
			return err
		}
		if p.WorkspaceID != nil {
			if _, err := q.ActivateWorkspacePrompt(ctx, store.ActivateWorkspacePromptParams{ID: id, WorkspaceID: &m.WorkspaceID}); err != nil {
				return err
			}
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "prompt.activate", "prompt", id.String(), map[string]any{"task": p.Task, "version": p.Version})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- workspace settings (local-only mode, embeddings, search) ----

type workspaceSettingsDTO struct {
	LocalOnly         bool                     `json:"local_only"`
	Embeddings        router.EmbeddingSettings `json:"embeddings"`
	Search            search.Settings          `json:"search"`
	SearchKeySet      bool                     `json:"search_key_set"`
	EncryptionEnabled bool                     `json:"encryption_enabled"`
	UpdatedAt         *time.Time               `json:"updated_at"`
}

func (s *server) loadWorkspaceSettings(ctx context.Context, q *store.Queries, ws uuid.UUID) (workspaceSettingsDTO, error) {
	d := workspaceSettingsDTO{EncryptionEnabled: s.box.Enabled()}
	row, err := q.GetWorkspaceSettings(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	d.LocalOnly, d.UpdatedAt = row.LocalOnly, &row.UpdatedAt
	_ = json.Unmarshal(orEmptyJSON(row.Embeddings), &d.Embeddings)
	_ = json.Unmarshal(orEmptyJSON(row.Search), &d.Search)
	st := d.Search
	switch st.Backend {
	case search.SearXNG:
		d.SearchKeySet = st.SearXNGURL != ""
	case search.GooglePSE:
		d.SearchKeySet = st.GooglePSECX != "" && (st.GooglePSEKeySecretID != nil || (st.GooglePSEKeyEnv != "" && s.env(st.GooglePSEKeyEnv) != ""))
	case search.Brave:
		d.SearchKeySet = st.APIKeySecretID != nil || (st.APIKeyEnv != "" && s.env(st.APIKeyEnv) != "")
	}
	return d, nil
}

func (s *server) handleGetWorkspaceSettings(c *gin.Context) {
	d, err := s.loadWorkspaceSettings(c.Request.Context(), store.New(s.pool), memberOf(c).WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, d)
}

func (s *server) handlePutWorkspaceSettings(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	var in struct {
		LocalOnly    bool                     `json:"local_only"`
		Embeddings   router.EmbeddingSettings `json:"embeddings"`
		Search       search.Settings          `json:"search"`
		SearchAPIKey *string                  `json:"search_api_key"`     // Brave key: stored encrypted
		GooglePSEKey *string                  `json:"google_pse_api_key"` // Google PSE key: stored encrypted
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	for _, e := range []string{in.Search.APIKeyEnv, in.Search.GooglePSEKeyEnv} {
		if e != "" && !envNameRe.MatchString(e) {
			s.fail(c, badRequest("search key env must be an env var name"))
			return
		}
	}
	if in.Search.Backend != "" {
		if err := oneOf("search.backend", in.Search.Backend, search.SearXNG, search.GooglePSE, search.Brave); err != nil {
			s.fail(c, err)
			return
		}
	}
	if in.Search.SearXNGURL != "" {
		if _, err := optURL("search.searxng_url", &in.Search.SearXNGURL); err != nil {
			s.fail(c, err)
			return
		}
	}
	if in.Search.Safesearch != "" {
		if err := oneOf("search.safesearch", in.Search.Safesearch, "off", "moderate", "strict"); err != nil {
			s.fail(c, err)
			return
		}
	}
	if in.Search.MonthlyBudget < 0 {
		s.fail(c, badRequest("search.monthly_budget cannot be negative"))
		return
	}
	ctx := c.Request.Context()
	var out workspaceSettingsDTO
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		prev, err := s.loadWorkspaceSettings(ctx, q, m.WorkspaceID)
		if err != nil {
			return err
		}
		e := in.Embeddings
		if e.ProviderID != "" {
			row, err := q.GetProvider(ctx, e.ProviderID)
			if err != nil {
				return badRequest("unknown embeddings provider")
			}
			if row.Kind != config.KindOpenAICompat {
				return badRequest("embeddings need an OpenAI-compatible provider (/v1/embeddings)")
			}
			if strings.TrimSpace(e.Model) == "" || e.Dimensions <= 0 || e.Dimensions > 16000 {
				return badRequest("embeddings need a model and dimensions (1-16000)")
			}
			if in.LocalOnly && !row.IsLocal {
				return &apiError{http.StatusConflict, "hosted_embeddings", "local-only mode needs a local embeddings provider; choose one under Embeddings first"}
			}
		}
		keep := func(paste *string, label string, dst **uuid.UUID, prevID *uuid.UUID) error {
			if paste != nil && strings.TrimSpace(*paste) != "" {
				id, err := s.createSecret(ctx, q, label+" ("+time.Now().UTC().Format("2006-01-02 15:04:05")+")", *paste, nil, u.ID)
				if err != nil {
					return err
				}
				*dst = &id
			} else if *dst == nil {
				*dst = prevID
			}
			return nil
		}
		if err := keep(in.SearchAPIKey, "Brave Search API key", &in.Search.APIKeySecretID, prev.Search.APIKeySecretID); err != nil {
			return err
		}
		if err := keep(in.GooglePSEKey, "Google Programmable Search API key", &in.Search.GooglePSEKeySecretID, prev.Search.GooglePSEKeySecretID); err != nil {
			return err
		}
		emb, _ := json.Marshal(e)
		search, _ := json.Marshal(in.Search)
		if _, err := q.UpsertWorkspaceSettings(ctx, store.UpsertWorkspaceSettingsParams{WorkspaceID: m.WorkspaceID, LocalOnly: in.LocalOnly, Embeddings: emb, Search: search}); err != nil {
			return err
		}
		// A different embedding model or size means every stored vector is stale.
		if e.ProviderID != "" && (e.Model != prev.Embeddings.Model || e.Dimensions != prev.Embeddings.Dimensions || e.ProviderID != prev.Embeddings.ProviderID) {
			if err := s.reembedAll(ctx, tx, q, m.WorkspaceID); err != nil {
				return err
			}
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "settings.workspace", "workspace", m.WorkspaceID.String(),
			map[string]any{"local_only": in.LocalOnly, "embeddings_model": e.Model})
		out, err = s.loadWorkspaceSettings(ctx, q, m.WorkspaceID)
		return err
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (s *server) reembedAll(ctx context.Context, tx pgx.Tx, q *store.Queries, ws uuid.UUID) error {
	items, err := q.ListPortfolioItems(ctx, store.ListPortfolioItemsParams{WorkspaceID: ws})
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := s.enqueueEmbed(ctx, tx, ws, jobs.EntityPortfolioItem, it.ID); err != nil {
			return err
		}
	}
	icps, err := q.ListICPs(ctx, store.ListICPsParams{WorkspaceID: ws})
	if err != nil {
		return err
	}
	for _, i := range icps {
		if err := s.enqueueEmbed(ctx, tx, ws, jobs.EntityICP, i.ID); err != nil {
			return err
		}
	}
	return nil
}

// ---- model runs ----

func (s *server) handleListRuns(c *gin.Context) {
	rows, err := store.New(s.pool).ListRecentModelRuns(c.Request.Context(), store.ListRecentModelRunsParams{WorkspaceID: memberOf(c).WorkspaceID, Limit: 100})
	if err != nil {
		s.fail(c, err)
		return
	}
	type runDTO struct {
		ID           uuid.UUID `json:"id"`
		Task         string    `json:"task"`
		ProviderID   string    `json:"provider_id"`
		Model        string    `json:"model"`
		InputTokens  *int32    `json:"input_tokens"`
		OutputTokens *int32    `json:"output_tokens"`
		CostUSD      *float64  `json:"cost_usd"`
		LatencyMs    *int32    `json:"latency_ms"`
		Error        *string   `json:"error"`
		Fallback     bool      `json:"fallback"`
		CreatedAt    time.Time `json:"created_at"`
	}
	out := make([]runDTO, 0, len(rows))
	for _, r := range rows {
		d := runDTO{r.ID, r.Task, r.ProviderID, r.Model, r.InputTokens, r.OutputTokens, nil, r.LatencyMs, r.Error, r.Fallback, r.CreatedAt}
		if f, err := r.CostUsd.Float64Value(); err == nil && f.Valid {
			d.CostUSD = &f.Float64
		}
		out = append(out, d)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func ptrUUID(u uuid.UUID) *uuid.UUID { return &u }
