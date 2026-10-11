package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/time/rate"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/auth"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Headers used by the API.
const (
	CSRFHeader      = "X-CSRF-Token"
	WorkspaceHeader = "X-Workspace-ID"
)

type sessionUser struct {
	ID    uuid.UUID
	Email string
	Name  string
	SID   string // sessions.id
}

type membership struct {
	WorkspaceID uuid.UUID
	Role        auth.Role
}

const (
	ctxUser      = "auth.user"
	ctxMember    = "auth.membership"
	maxLoginBody = 4 << 10
)

func (s *server) sessionCookie() string {
	if s.secure {
		return "__Host-session"
	}
	return "session"
}

func (s *server) csrfCookie() string {
	if s.secure {
		return "__Host-csrf"
	}
	return "csrf"
}

func (s *server) setCookie(c *gin.Context, name, value string, maxAge int, httpOnly bool) {
	// #nosec G124 -- Secure follows server.base_url (https in prod); the LAN NodePort
	// deploy is plain HTTP, where browsers drop Secure cookies. HttpOnly is false
	// only for the CSRF double-submit cookie.
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: httpOnly,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// requireSession loads the user from the session cookie.
func (s *server) requireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok, err := c.Cookie(s.sessionCookie())
		if err != nil || tok == "" {
			s.fail(c, errUnauthed)
			return
		}
		sid := auth.SessionID(tok)
		row, err := store.New(s.pool).GetSessionUser(c.Request.Context(), sid)
		if errors.Is(err, pgx.ErrNoRows) {
			s.fail(c, errUnauthed)
			return
		}
		if err != nil {
			s.fail(c, err)
			return
		}
		c.Set(ctxUser, sessionUser{ID: row.ID, Email: row.Email, Name: row.Name, SID: sid})
		c.Next()
	}
}

// requireCSRF enforces the double-submit token on state-changing methods.
func (s *server) requireCSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		cookie, _ := c.Cookie(s.csrfCookie())
		if !auth.TokensEqual(cookie, c.GetHeader(CSRFHeader)) {
			writeError(c, http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token")
			return
		}
		c.Next()
	}
}

// requireWorkspace resolves the workspace from X-Workspace-ID (or the user's
// first workspace when absent) and checks membership.
func (s *server) requireWorkspace() gin.HandlerFunc {
	return func(c *gin.Context) {
		u := c.MustGet(ctxUser).(sessionUser)
		q := store.New(s.pool)
		var wsID uuid.UUID
		h := c.GetHeader(WorkspaceHeader)
		if h == "" && c.Request.Method == http.MethodGet {
			h = c.Query("workspace") // EventSource cannot set headers
		}
		if h != "" {
			id, err := uuid.Parse(h)
			if err != nil {
				s.fail(c, badRequest(WorkspaceHeader+" must be a UUID"))
				return
			}
			wsID = id
		} else {
			list, err := q.ListWorkspacesForUser(c.Request.Context(), u.ID)
			if err != nil {
				s.fail(c, err)
				return
			}
			if len(list) == 0 {
				s.fail(c, errForbidden)
				return
			}
			wsID = list[0].ID
		}
		m, err := q.GetMembership(c.Request.Context(), store.GetMembershipParams{WorkspaceID: wsID, UserID: u.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			s.fail(c, errForbidden)
			return
		}
		if err != nil {
			s.fail(c, err)
			return
		}
		c.Set(ctxMember, membership{WorkspaceID: m.WorkspaceID, Role: auth.Role(m.Role)})
		c.Next()
	}
}

// requireRole aborts unless the member's role allows min.
func requireRole(min auth.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		m := c.MustGet(ctxMember).(membership)
		if !m.Role.Allows(min) {
			writeError(c, errForbidden.status, errForbidden.code, errForbidden.message)
			return
		}
		c.Next()
	}
}

func userOf(c *gin.Context) sessionUser  { return c.MustGet(ctxUser).(sessionUser) }
func memberOf(c *gin.Context) membership { return c.MustGet(ctxMember).(membership) }

// loginLimiter throttles login attempts per email: a burst of 5, then one
// attempt every 30 seconds. Keyed by email because, behind the web proxy,
// every request shares a source IP.
type loginLimiter struct {
	mu sync.Mutex
	m  map[string]*limiterEntry
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{m: map[string]*limiterEntry{}} }

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.m) > 10000 { // bound memory
		for k, e := range l.m {
			if now.Sub(e.seen) > time.Hour {
				delete(l.m, k)
			}
		}
	}
	e, ok := l.m[key]
	if !ok {
		e = &limiterEntry{lim: rate.NewLimiter(rate.Every(30*time.Second), 5)}
		l.m[key] = e
	}
	e.seen = now
	return e.lim.Allow()
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userDTO struct {
	ID    uuid.UUID `json:"id"`
	Email string    `json:"email"`
	Name  string    `json:"name"`
}

type workspaceDTO struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Role string    `json:"role"`
}

type sessionDTO struct {
	User       userDTO        `json:"user"`
	Workspaces []workspaceDTO `json:"workspaces"`
	CSRFToken  string         `json:"csrf_token"`
}

func (s *server) handleLogin(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxLoginBody)
	var req loginRequest
	if err := bindJSON(c, &req); err != nil {
		s.fail(c, err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		s.fail(c, badRequest("email and password are required"))
		return
	}
	if !s.limiter.allow(email) {
		writeError(c, http.StatusTooManyRequests, "rate_limited", "too many sign-in attempts; try again shortly")
		return
	}
	ctx := c.Request.Context()
	q := store.New(s.pool)
	u, err := q.GetUserByEmail(ctx, email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		auth.BurnVerify(req.Password)
		s.log.WarnContext(ctx, "login failed", "reason", "unknown_user", "request_id", RequestIDFrom(ctx))
		s.fail(c, &apiError{http.StatusUnauthorized, "invalid_credentials", "invalid email or password"})
		return
	case err != nil:
		s.fail(c, err)
		return
	}
	if err := auth.VerifyPassword(req.Password, u.PasswordHash); err != nil {
		s.log.WarnContext(ctx, "login failed", "reason", "bad_password", "user_id", u.ID, "request_id", RequestIDFrom(ctx))
		s.fail(c, &apiError{http.StatusUnauthorized, "invalid_credentials", "invalid email or password"})
		return
	}

	s.startSession(c, q, u)
}

func (s *server) handleLogout(c *gin.Context) {
	u := userOf(c)
	ctx := c.Request.Context()
	q := store.New(s.pool)
	if err := q.DeleteSession(ctx, u.SID); err != nil {
		s.fail(c, err)
		return
	}
	if ws, err := q.ListWorkspacesForUser(ctx, u.ID); err == nil {
		for _, w := range ws {
			s.audit(ctx, q, w.ID, &u.ID, "logout", "user", u.ID.String(), nil)
		}
	}
	s.setCookie(c, s.sessionCookie(), "", -1, true)
	s.setCookie(c, s.csrfCookie(), "", -1, false)
	c.Status(http.StatusNoContent)
}

func (s *server) handleMe(c *gin.Context) {
	u := userOf(c)
	ws, err := store.New(s.pool).ListWorkspacesForUser(c.Request.Context(), u.ID)
	if err != nil {
		s.fail(c, err)
		return
	}
	csrf, _ := c.Cookie(s.csrfCookie())
	if csrf == "" {
		if csrf, err = auth.NewToken(); err != nil {
			s.fail(c, err)
			return
		}
		s.setCookie(c, s.csrfCookie(), csrf, int(auth.SessionTTL.Seconds()), false)
	}
	c.JSON(http.StatusOK, sessionDTO{User: userDTO{u.ID, u.Email, u.Name}, Workspaces: toWorkspaceDTOs(ws), CSRFToken: csrf})
}

func toWorkspaceDTOs(ws []store.ListWorkspacesForUserRow) []workspaceDTO {
	out := make([]workspaceDTO, 0, len(ws))
	for _, w := range ws {
		out = append(out, workspaceDTO{ID: w.ID, Name: w.Name, Role: string(w.Role)})
	}
	return out
}

// audit writes an audit_log row. Failures are logged, not returned: auditing
// must not break the user action, but a failure is an operational alert.
func (s *server) audit(ctx context.Context, q *store.Queries, ws uuid.UUID, user *uuid.UUID, action, targetType, targetID string, meta map[string]any) {
	b := []byte("{}")
	if meta != nil {
		if mb, err := json.Marshal(meta); err == nil {
			b = mb
		}
	}
	if err := q.InsertAudit(ctx, store.InsertAuditParams{
		WorkspaceID: ws, UserID: user, Action: action, TargetType: &targetType, TargetID: &targetID, Meta: b,
	}); err != nil {
		s.log.ErrorContext(ctx, "audit write failed", "action", action, "err", err)
	}
}
