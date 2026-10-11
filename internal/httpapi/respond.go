package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// apiError is returned by handlers and rendered by fail.
type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.message }

func badRequest(msg string) *apiError {
	return &apiError{http.StatusBadRequest, "invalid_request", msg}
}

var (
	errNotFound  = &apiError{http.StatusNotFound, "not_found", "not found"}
	errForbidden = &apiError{http.StatusForbidden, "forbidden", "insufficient role for this action"}
	errUnauthed  = &apiError{http.StatusUnauthorized, "unauthenticated", "sign in required"}
)

// fail renders err as the standard error body. Unknown errors become 500s
// and are logged without being echoed to the client.
func (s *server) fail(c *gin.Context, err error) {
	var ae *apiError
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &ae):
	case errors.Is(err, pgx.ErrNoRows):
		ae = errNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		ae = &apiError{http.StatusConflict, "conflict", "an item with these values already exists"}
	default:
		s.log.ErrorContext(c.Request.Context(), "request failed",
			"request_id", RequestIDFrom(c.Request.Context()), "err", err)
		ae = &apiError{http.StatusInternalServerError, "internal", "internal server error"}
	}
	writeError(c, ae.status, ae.code, ae.message)
}

func pathUUID(c *gin.Context, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		return uuid.Nil, errNotFound
	}
	return id, nil
}

// bindJSON decodes the body into v, rejecting non-JSON content types.
func bindJSON(c *gin.Context, v any) error {
	if ct := c.ContentType(); ct != "application/json" {
		return &apiError{http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json"}
	}
	if err := c.ShouldBindJSON(v); err != nil {
		return badRequest("invalid JSON body: " + err.Error())
	}
	return nil
}

// Field validation helpers. Each trims in place and returns an error naming
// the field.

func reqString(field string, v *string, max int) error {
	*v = strings.TrimSpace(*v)
	if *v == "" {
		return badRequest(field + " is required")
	}
	if len(*v) > max {
		return badRequest(field + " is too long")
	}
	return nil
}

func optString(field string, v *string, max int) error {
	if v == nil {
		return nil
	}
	*v = strings.TrimSpace(*v)
	if len(*v) > max {
		return badRequest(field + " is too long")
	}
	return nil
}

// cleanList trims, drops empties and duplicates, and bounds size.
func cleanList(field string, in []string, maxItems, maxLen int) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		if len(s) > maxLen {
			return nil, badRequest(field + " has an item that is too long")
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	if len(out) > maxItems {
		return nil, badRequest(field + " has too many items")
	}
	return out, nil
}

// optURL validates an optional absolute http(s) URL; empty becomes nil.
func optURL(field string, v *string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		return nil, nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(s) > 2000 {
		return nil, badRequest(field + " must be an absolute http(s) URL")
	}
	return &s, nil
}

func oneOf(field, v string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return badRequest(field + " must be one of: " + strings.Join(allowed, ", "))
}
