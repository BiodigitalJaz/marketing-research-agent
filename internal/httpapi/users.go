package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/auth"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Users are managed per workspace. The app never sends email: an admin adds
// a person with a temporary password and shares it with them directly.
// Removing someone deletes their membership; the user row stays so their
// past work keeps its attribution.

type memberDTO struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	You       bool      `json:"you"`
}

var errLastAdmin = &apiError{http.StatusConflict, "last_admin", "a workspace needs at least one admin; make someone else an admin first"}

func validRole(r string) bool {
	return r == string(auth.RoleAdmin) || r == string(auth.RoleEditor) || r == string(auth.RoleViewer)
}

func (s *server) handleListMembers(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	rows, err := store.New(s.pool).ListMembers(c.Request.Context(), m.WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]memberDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, memberDTO{r.ID, r.Email, r.Name, string(r.Role), r.CreatedAt, r.ID == u.ID})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// handleAddMember adds a person to the workspace. A new email creates the
// user with the given temporary password. An existing user who belongs to no
// workspace gets the new password; one who belongs to another workspace
// keeps theirs (an admin here cannot reset it).
func (s *server) handleAddMember(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	var in struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	in.Email, in.Name = strings.ToLower(strings.TrimSpace(in.Email)), strings.TrimSpace(in.Name)
	switch {
	case in.Email == "" || !strings.Contains(in.Email, "@"):
		s.fail(c, badRequest("a valid email is required"))
		return
	case !validRole(in.Role):
		s.fail(c, badRequest("role must be admin, editor, or viewer"))
		return
	}
	ctx := c.Request.Context()
	var added store.User
	keptPassword := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		existing, err := q.GetUserByEmail(ctx, in.Email)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if len(in.Password) < auth.MinPasswordLen {
				return badRequest("set a temporary password of at least 12 characters")
			}
			hash, err := auth.HashPassword(in.Password, auth.DefaultArgon2)
			if err != nil {
				return err
			}
			name := in.Name
			if name == "" {
				name = in.Email[:strings.IndexByte(in.Email, '@')]
			}
			if added, err = q.CreateUser(ctx, store.CreateUserParams{Email: in.Email, Name: name, PasswordHash: hash}); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			added = existing
			if _, err := q.GetMembership(ctx, store.GetMembershipParams{WorkspaceID: m.WorkspaceID, UserID: existing.ID}); err == nil {
				return &apiError{http.StatusConflict, "already_member", "this person is already in the workspace"}
			}
			others, err := q.CountOtherMemberships(ctx, store.CountOtherMembershipsParams{UserID: existing.ID, WorkspaceID: m.WorkspaceID})
			if err != nil {
				return err
			}
			keptPassword = others > 0 || in.Password == ""
			if !keptPassword {
				if len(in.Password) < auth.MinPasswordLen {
					return badRequest("set a temporary password of at least 12 characters")
				}
				hash, err := auth.HashPassword(in.Password, auth.DefaultArgon2)
				if err != nil {
					return err
				}
				if err := q.SetUserPassword(ctx, store.SetUserPasswordParams{ID: existing.ID, PasswordHash: hash}); err != nil {
					return err
				}
			}
		}
		if err := q.AddMembership(ctx, store.AddMembershipParams{WorkspaceID: m.WorkspaceID, UserID: added.ID, Role: store.MemberRole(in.Role)}); err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "member.add", "user", added.ID.String(), map[string]any{"role": in.Role})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	// kept_password: the person already had an account elsewhere and signs in
	// with their existing password, not the one entered here.
	c.JSON(http.StatusCreated, gin.H{"member": memberDTO{added.ID, added.Email, added.Name, in.Role, added.CreatedAt, false}, "kept_password": keptPassword})
}

// handleUpdateMember changes a member's role and, optionally, their name.
func (s *server) handleUpdateMember(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	var in struct {
		Role string  `json:"role"`
		Name *string `json:"name"`
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if !validRole(in.Role) {
		s.fail(c, badRequest("role must be admin, editor, or viewer"))
		return
	}
	ctx := c.Request.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		admins, err := q.LockWorkspaceAdmins(ctx, m.WorkspaceID)
		if err != nil {
			return err
		}
		if in.Role != string(auth.RoleAdmin) && slices.Contains(admins, id) && len(admins) == 1 {
			return errLastAdmin
		}
		n, err := q.SetMemberRole(ctx, store.SetMemberRoleParams{WorkspaceID: m.WorkspaceID, UserID: id, Role: store.MemberRole(in.Role)})
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotFound
		}
		if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
			if err := q.SetUserName(ctx, store.SetUserNameParams{ID: id, Name: strings.TrimSpace(*in.Name)}); err != nil {
				return err
			}
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "member.update", "user", id.String(), map[string]any{"role": in.Role})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	s.writeMember(c, m.WorkspaceID, id, u.ID)
}

// handleRemoveMember removes a person from the workspace. When they belong
// to no other workspace, their sessions end too.
func (s *server) handleRemoveMember(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		admins, err := q.LockWorkspaceAdmins(ctx, m.WorkspaceID)
		if err != nil {
			return err
		}
		if slices.Contains(admins, id) && len(admins) == 1 {
			return errLastAdmin
		}
		n, err := q.DeleteMembership(ctx, store.DeleteMembershipParams{WorkspaceID: m.WorkspaceID, UserID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotFound
		}
		others, err := q.CountOtherMemberships(ctx, store.CountOtherMembershipsParams{UserID: id, WorkspaceID: m.WorkspaceID})
		if err != nil {
			return err
		}
		if others == 0 {
			if err := q.DeleteUserSessions(ctx, id); err != nil {
				return err
			}
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "member.remove", "user", id.String(), nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// handleResetMemberPassword sets a temporary password and ends the person's
// sessions. Only for people who belong to no other workspace.
func (s *server) handleResetMemberPassword(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if len(in.Password) < auth.MinPasswordLen {
		s.fail(c, badRequest("the password must be at least 12 characters"))
		return
	}
	ctx := c.Request.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.GetMembership(ctx, store.GetMembershipParams{WorkspaceID: m.WorkspaceID, UserID: id}); err != nil {
			return err
		}
		others, err := q.CountOtherMemberships(ctx, store.CountOtherMembershipsParams{UserID: id, WorkspaceID: m.WorkspaceID})
		if err != nil {
			return err
		}
		if others > 0 {
			return &apiError{http.StatusForbidden, "other_workspaces", "this person also belongs to other workspaces; they can change their own password, or an admin of every workspace they belong to can reset it"}
		}
		hash, err := auth.HashPassword(in.Password, auth.DefaultArgon2)
		if err != nil {
			return err
		}
		if err := q.SetUserPassword(ctx, store.SetUserPasswordParams{ID: id, PasswordHash: hash}); err != nil {
			return err
		}
		if id == u.ID {
			err = q.DeleteOtherUserSessions(ctx, store.DeleteOtherUserSessionsParams{UserID: id, ID: u.SID})
		} else {
			err = q.DeleteUserSessions(ctx, id)
		}
		if err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "member.reset_password", "user", id.String(), nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *server) writeMember(c *gin.Context, ws, id, me uuid.UUID) {
	r, err := store.New(s.pool).GetMember(c.Request.Context(), store.GetMemberParams{WorkspaceID: ws, UserID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, memberDTO{r.ID, r.Email, r.Name, string(r.Role), r.CreatedAt, r.ID == me})
}

// handleChangePassword changes the signed-in user's own password and signs
// them out everywhere else.
func (s *server) handleChangePassword(c *gin.Context) {
	u := userOf(c)
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if len(in.New) < auth.MinPasswordLen {
		s.fail(c, badRequest("the new password must be at least 12 characters"))
		return
	}
	if !s.limiter.allow("password:" + u.ID.String()) {
		writeError(c, http.StatusTooManyRequests, "rate_limited", "too many attempts; try again shortly")
		return
	}
	ctx := c.Request.Context()
	q := store.New(s.pool)
	row, err := q.GetUserByID(ctx, u.ID)
	if err != nil {
		s.fail(c, err)
		return
	}
	if err := auth.VerifyPassword(in.Current, row.PasswordHash); err != nil {
		s.fail(c, &apiError{http.StatusUnauthorized, "invalid_credentials", "the current password is not correct"})
		return
	}
	hash, err := auth.HashPassword(in.New, auth.DefaultArgon2)
	if err != nil {
		s.fail(c, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tq := store.New(tx)
		if err := tq.SetUserPassword(ctx, store.SetUserPasswordParams{ID: u.ID, PasswordHash: hash}); err != nil {
			return err
		}
		return tq.DeleteOtherUserSessions(ctx, store.DeleteOtherUserSessionsParams{UserID: u.ID, ID: u.SID})
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	ws, _ := q.ListWorkspacesForUser(ctx, u.ID)
	for _, w := range ws {
		s.audit(ctx, q, w.ID, &u.ID, "password.change", "user", u.ID.String(), nil)
	}
	c.Status(http.StatusNoContent)
}

// handleRenameWorkspace renames the workspace; prompts use the new name as
// the brand name from the next call on.
func (s *server) handleRenameWorkspace(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	var in struct {
		Name string `json:"name"`
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 200 {
		s.fail(c, badRequest("the name must be 1 to 200 characters"))
		return
	}
	ctx := c.Request.Context()
	q := store.New(s.pool)
	w, err := q.RenameWorkspace(ctx, store.RenameWorkspaceParams{ID: m.WorkspaceID, Name: name})
	if err != nil {
		s.fail(c, err)
		return
	}
	s.audit(ctx, q, m.WorkspaceID, &u.ID, "workspace.rename", "workspace", w.ID.String(), map[string]any{"name": name})
	c.JSON(http.StatusOK, gin.H{"id": w.ID, "name": w.Name})
}
