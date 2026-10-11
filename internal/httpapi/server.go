// Package httpapi exposes the REST + SSE API.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/auth"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/collect"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/config"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/mcp"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
)

// Pinger reports database reachability; *pgxpool.Pool satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// JobClient enqueues River jobs inside a transaction and lists them (for
// progress reporting); *river.Client satisfies it.
type JobClient interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
	JobList(ctx context.Context, params *river.JobListParams) (*river.JobListResult, error)
	JobCancel(ctx context.Context, jobID int64) (*rivertype.JobRow, error)
}

// Deps are the handler dependencies. When Pool is nil only the health
// endpoints are mounted (used by lightweight tests).
type Deps struct {
	Log  *slog.Logger
	DB   Pinger
	Pool *pgxpool.Pool
	Jobs JobClient
	// SecureCookies sets Secure and the __Host- prefix on cookies. Derive it
	// from server.base_url: true for https.
	SecureCookies bool
	// Router resolves LLM routes and builds providers; Secrets encrypts
	// UI-entered keys; Env resolves env-var key references.
	Router  *router.Router
	Secrets *secrets.Box
	Env     func(string) string
	// MCPConnect opens MCP sessions; nil uses mcp.Connect (tests inject).
	MCPConnect func(context.Context, mcp.ServerConfig) (*mcp.Conn, error)
	// Collect runs source tests (dry runs) for the sources API.
	Collect *collect.Service
	// Location is the daily-inbox timezone (default UTC).
	Location *time.Location
	// Config supplies first-run setup's defaults (providers, routes,
	// embeddings, search); nil disables setup.
	Config *config.Config
}

type server struct {
	log        *slog.Logger
	pool       *pgxpool.Pool
	jobs       JobClient
	secure     bool
	limiter    *loginLimiter
	router     *router.Router
	box        *secrets.Box
	env        func(string) string
	mcpConnect func(context.Context, mcp.ServerConfig) (*mcp.Conn, error)
	hub        *researchHub
	collect    *collect.Service
	loc        *time.Location
	cfg        *config.Config
}

// RequestIDHeader carries the request id in and out.
const RequestIDHeader = "X-Request-ID"

// NewRouter builds the HTTP handler. Liveness and readiness are served at
// the root (for orchestrators) and under /api/v1 (for the web app). Callers
// should set gin.SetMode(gin.ReleaseMode) outside development.
func NewRouter(d Deps) http.Handler {
	r := gin.New()
	r.HandleMethodNotAllowed = true
	_ = r.SetTrustedProxies(nil) // never trust X-Forwarded-For for ClientIP
	r.Use(requestID(), requestLogger(d.Log), recovery(d.Log))

	v1 := r.Group("/api/v1")
	for _, g := range []gin.IRoutes{r, v1} {
		g.GET("/health", handleHealth)
		g.GET("/ready", handleReady(d.DB))
	}
	if d.Pool != nil {
		s := &server{log: d.Log, pool: d.Pool, jobs: d.Jobs, secure: d.SecureCookies, limiter: newLoginLimiter(),
			router: d.Router, box: d.Secrets, env: d.Env, mcpConnect: d.MCPConnect, collect: d.Collect, loc: d.Location, cfg: d.Config}
		if s.loc == nil {
			s.loc = time.UTC
		}
		if s.env == nil {
			s.env = func(string) string { return "" }
		}
		if s.mcpConnect == nil {
			s.mcpConnect = mcp.Connect
		}
		s.hub = newResearchHub(d.Pool, d.Log)
		s.routes(v1)
	}
	r.NoRoute(func(c *gin.Context) {
		writeError(c, http.StatusNotFound, "not_found", "route not found")
	})
	r.NoMethod(func(c *gin.Context) {
		writeError(c, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	return r
}

func (s *server) routes(v1 *gin.RouterGroup) {
	v1.POST("/auth/login", s.handleLogin)
	v1.GET("/setup/status", s.handleSetupStatus)
	v1.POST("/setup", s.handleSetup)

	authed := v1.Group("", s.requireSession(), s.requireCSRF())
	authed.POST("/auth/logout", s.handleLogout)
	authed.GET("/auth/me", s.handleMe)
	authed.POST("/auth/password", s.handleChangePassword)

	ws := authed.Group("", s.requireWorkspace())
	view, edit := requireRole(auth.RoleViewer), requireRole(auth.RoleEditor)

	ws.GET("/setup/checklist", view, s.handleChecklist)
	ws.GET("/brand/profile", view, s.handleGetProfile)
	ws.PUT("/brand/profile", edit, s.handlePutProfile)
	ws.POST("/brand/import", edit, s.handleImportBrandPack)
	ws.GET("/brand/export", view, s.handleExportBrandPack)

	ws.GET("/portfolio", view, s.handleListPortfolio)
	ws.GET("/portfolio/:id", view, s.handleGetPortfolio)
	ws.POST("/portfolio", edit, s.handleSavePortfolio)
	ws.PUT("/portfolio/:id", edit, s.handleSavePortfolio)
	ws.DELETE("/portfolio/:id", edit, s.handleDeletePortfolio)

	ws.GET("/icps", view, s.handleListICPs)
	ws.POST("/icps", edit, s.handleSaveICP)
	ws.PUT("/icps/:id", edit, s.handleSaveICP)
	ws.DELETE("/icps/:id", edit, s.handleDeleteICP)

	ws.GET("/keywords", view, s.handleListKeywords)
	ws.POST("/keywords", edit, s.handleSaveKeyword)
	ws.PUT("/keywords/:id", edit, s.handleSaveKeyword)
	ws.DELETE("/keywords/:id", edit, s.handleDeleteKeyword)

	ws.GET("/competitors", view, s.handleListCompetitors)
	ws.POST("/competitors", edit, s.handleSaveCompetitor)
	ws.PUT("/competitors/:id", edit, s.handleSaveCompetitor)
	ws.DELETE("/competitors/:id", edit, s.handleDeleteCompetitor)

	ws.GET("/sources", view, s.handleListSources)
	ws.POST("/sources", requireRole(auth.RoleAdmin), s.handleSaveSource)
	ws.PUT("/sources/:id", requireRole(auth.RoleAdmin), s.handleSaveSource)
	ws.DELETE("/sources/:id", requireRole(auth.RoleAdmin), s.handleDeleteSource)
	ws.POST("/sources/:id/test", requireRole(auth.RoleAdmin), s.handleTestSource)

	ws.GET("/monitors", view, s.handleListMonitors)
	ws.POST("/monitors", edit, s.handleSaveMonitor)
	ws.PUT("/monitors/:id", edit, s.handleSaveMonitor)
	ws.DELETE("/monitors/:id", edit, s.handleDeleteMonitor)
	ws.POST("/monitors/:id/run", edit, s.handleRunMonitor)
	ws.GET("/monitors/:id/progress", view, s.handleMonitorProgress)

	ws.GET("/items", view, s.handleListItems)
	ws.POST("/items/process", edit, s.handleProcessPending)

	ws.GET("/opportunities", view, s.handleListOpportunities)
	ws.GET("/opportunities/:id", view, s.handleGetOpportunity)
	ws.POST("/opportunities/:id/status", edit, s.handleSetStatus)
	ws.POST("/opportunities/:id/events", view, s.handleLogEvent)
	ws.POST("/opportunities/:id/drafts", edit, s.handleRequestDrafts)

	ws.GET("/research", view, s.handleListResearch)
	ws.POST("/research", edit, s.handleCreateResearch)
	ws.GET("/research/:id", view, s.handleGetResearch)
	ws.GET("/research/:id/stream", view, s.handleResearchStream)
	ws.POST("/research/:id/messages", edit, s.handleResearchMessage)
	ws.POST("/research/:id/cancel", edit, s.handleCancelResearch)
	ws.POST("/research/:id/archive", edit, s.handleArchiveResearch)
	ws.DELETE("/research/:id", edit, s.handleDeleteResearch)
	ws.GET("/reports", view, s.handleListReports)
	ws.GET("/reports/:id", view, s.handleGetReport)
	ws.GET("/reports/:id/export", view, s.handleExportReport)
	ws.GET("/knowledge/memory-bank", view, s.handleMemoryBank)

	admin := ws.Group("/settings", requireRole(auth.RoleAdmin))
	admin.GET("/secrets", s.handleListSecrets)
	admin.POST("/secrets", s.handleCreateSecret)
	admin.PUT("/secrets/:id", s.handleUpdateSecret)
	admin.DELETE("/secrets/:id", s.handleDeleteSecret)

	admin.GET("/providers", s.handleListProviders)
	admin.POST("/providers", s.handleSaveProvider)
	admin.PUT("/providers/:id", s.handleSaveProvider)
	admin.DELETE("/providers/:id", s.handleDeleteProvider)
	admin.POST("/providers/:id/test", s.handleTestProvider)
	admin.GET("/providers/:id/discover", s.handleDiscoverModels)
	admin.POST("/embeddings/probe", s.handleProbeEmbeddings)

	admin.GET("/models", s.handleListModels)
	admin.POST("/models", s.handleSaveModel)
	admin.DELETE("/models", s.handleDeleteModel)

	admin.GET("/routes", s.handleListRoutes)
	admin.PUT("/routes/:task", s.handleSaveRoute)

	admin.GET("/prompts", s.handleListPrompts)
	admin.POST("/prompts", s.handleCreatePrompt)
	admin.POST("/prompts/:id/activate", s.handleActivatePrompt)

	admin.GET("/mcp-servers", s.handleListMCPServers)
	admin.POST("/mcp-servers", s.handleSaveMCPServer)
	admin.PUT("/mcp-servers/:id", s.handleSaveMCPServer)
	admin.DELETE("/mcp-servers/:id", s.handleDeleteMCPServer)
	admin.POST("/mcp-servers/:id/test", s.handleTestMCPServer)

	admin.GET("/knowledge", s.handleGetKnowledge)
	admin.POST("/knowledge", s.handleSaveKnowledge)
	admin.PUT("/knowledge/:id", s.handleSaveKnowledge)
	admin.DELETE("/knowledge/:id", s.handleDeleteKnowledge)
	admin.POST("/knowledge/:id/sync", s.handleSyncKnowledge)
	admin.GET("/knowledge/:id/docs", s.handleKnowledgeDocs)
	admin.POST("/knowledge/rebuild", s.handleRebuildMemoryBank)

	admin.GET("/lanes", s.handleListLanes)
	admin.PUT("/lanes/:lane", s.handleSaveLane)

	admin.GET("/users", s.handleListMembers)
	admin.POST("/users", s.handleAddMember)
	admin.PUT("/users/:id", s.handleUpdateMember)
	admin.DELETE("/users/:id", s.handleRemoveMember)
	admin.POST("/users/:id/password", s.handleResetMemberPassword)
	admin.PUT("/workspace/name", s.handleRenameWorkspace)
	admin.GET("/workspace", s.handleGetWorkspaceSettings)
	admin.PUT("/workspace", s.handlePutWorkspaceSettings)
	admin.GET("/runs", s.handleListRuns)
	admin.POST("/playground", s.handlePlayground)
}

type statusResponse struct {
	Status string `json:"status"`
}

func handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, statusResponse{Status: "ok"})
}

func handleReady(db Pinger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			writeError(c, http.StatusServiceUnavailable, "db_unavailable", "database is not reachable")
			return
		}
		c.JSON(http.StatusOK, statusResponse{Status: "ready"})
	}
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}

type ctxKey struct{}

// RequestIDFrom returns the request id stored in ctx, if any.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// requestID accepts a well-formed inbound X-Request-ID or generates one, and
// stores it on the request context and response header.
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if !validRequestID.MatchString(id) {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxKey{}, id))
		c.Header(RequestIDHeader, id)
		c.Next()
	}
}

func requestLogger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		level := slog.LevelInfo
		if p := c.Request.URL.Path; p == "/health" || p == "/ready" {
			level = slog.LevelDebug
		}
		log.Log(c.Request.Context(), level, "http request",
			"request_id", RequestIDFrom(c.Request.Context()),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"route", c.FullPath(),
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}

// recovery turns panics into a logged 500 with the standard error body.
func recovery(log *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, rec any) {
		log.ErrorContext(c.Request.Context(), "panic in handler",
			"request_id", RequestIDFrom(c.Request.Context()), "panic", rec)
		writeError(c, http.StatusInternalServerError, "internal", "internal server error")
	})
}
