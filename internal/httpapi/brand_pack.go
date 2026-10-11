package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/auth"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/brand"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/seed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

const maxPackBody = 1 << 20

// errDryRun rolls back a preview import.
var errDryRun = errors.New("dry run")

// handleImportBrandPack applies a brand pack (YAML or JSON) to the
// workspace. dry_run reports what would change without writing.
func (s *server) handleImportBrandPack(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPackBody)
	var in struct {
		Pack   string `json:"pack"`
		Mode   string `json:"mode"`
		DryRun bool   `json:"dry_run"`
		// RenameWorkspace renames the workspace to the pack's name (admins):
		// prompts call the brand by the workspace name.
		RenameWorkspace bool `json:"rename_workspace"`
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if in.Mode == "" {
		in.Mode = seed.ImportMerge
	}
	if in.Mode != seed.ImportMerge && in.Mode != seed.ImportReplace {
		s.fail(c, badRequest("mode must be merge or replace"))
		return
	}
	pack, err := brand.ParsePack([]byte(in.Pack))
	if err != nil {
		s.fail(c, badRequest(err.Error()))
		return
	}
	rename := in.RenameWorkspace && strings.TrimSpace(pack.Name) != ""
	if rename && m.Role != auth.RoleAdmin {
		s.fail(c, errForbidden)
		return
	}
	ctx := c.Request.Context()
	var sum seed.ImportSummary
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if sum, err = seed.ImportPack(ctx, tx, s.jobs, m.WorkspaceID, pack, in.Mode); err != nil {
			return err
		}
		if in.DryRun {
			return errDryRun
		}
		if rename {
			if _, err := store.New(tx).RenameWorkspace(ctx, store.RenameWorkspaceParams{ID: m.WorkspaceID, Name: strings.TrimSpace(pack.Name)}); err != nil {
				return err
			}
		}
		s.audit(ctx, store.New(tx), m.WorkspaceID, &u.ID, "brand.import", "brand_profile", m.WorkspaceID.String(),
			map[string]any{"mode": in.Mode, "pack": pack.Name, "renamed": rename, "keywords": sum.Keywords, "competitors": sum.Competitors, "icps": sum.ICPs, "portfolio": sum.Portfolio})
		return nil
	})
	if err != nil && !errors.Is(err, errDryRun) {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": pack.Name, "dry_run": in.DryRun, "renamed": rename && !in.DryRun, "summary": sum})
}

var unsafeFilename = regexp.MustCompile(`[^a-z0-9]+`)

// handleExportBrandPack downloads the workspace's brand as a YAML pack.
func (s *server) handleExportBrandPack(c *gin.Context) {
	m := memberOf(c)
	ctx := c.Request.Context()
	q := store.New(s.pool)
	w, err := q.GetWorkspace(ctx, m.WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	pack, err := seed.ExportPack(ctx, q, m.WorkspaceID, w.Name)
	if err != nil {
		s.fail(c, err)
		return
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(pack); err != nil {
		s.fail(c, err)
		return
	}
	body := buf.Bytes()
	name := strings.Trim(unsafeFilename.ReplaceAllString(strings.ToLower(w.Name), "-"), "-")
	if name == "" {
		name = "brand"
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-brand-pack.yaml"`, name))
	header := "# Brand pack exported from " + w.Name + ". Import it on the Brand page or with: seed -brand-file <this file>\n"
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", append([]byte(header), body...))
}
