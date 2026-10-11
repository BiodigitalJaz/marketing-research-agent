// Command seed bootstraps an install without the setup wizard: a workspace,
// its admin user, lane configs, providers and routes from config, the shipped
// prompts, an optional brand pack, and starter sources. Idempotent.
//
// Flags (after -config):
//
//	-workspace NAME       workspace to seed (default: the admin's first, else
//	                      the brand pack's name, else "My workspace")
//	-brand-file PATH      import a brand pack (YAML or JSON)
//	-brand-example NAME   import an embedded example pack (e.g. benchwright)
//
// Admin credentials come from RESEARCH_AGENT_ADMIN_EMAIL,
// RESEARCH_AGENT_ADMIN_PASSWORD (min 12 chars; only used when creating the
// user), and RESEARCH_AGENT_ADMIN_NAME.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/BiodigitalJaz/marketing-research-agent/examples"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/app"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/brand"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/config"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/db"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/seed"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var (
	workspaceName = flag.String("workspace", "", "workspace to seed")
	brandFile     = flag.String("brand-file", "", "brand pack to import (YAML or JSON)")
	brandExample  = flag.String("brand-example", "", "embedded example brand pack to import (one of: "+strings.Join(examples.BrandPacks(), ", ")+")")
)

func run() error {
	cfg, log, err := app.Bootstrap("seed") // parses the flags above too
	if err != nil {
		return err
	}
	pack, err := loadPack(*brandFile, *brandExample)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer pool.Close()

	ins, err := jobs.NewInsertClient(pool, log)
	if err != nil {
		return err
	}
	return seed.Run(ctx, pool, ins, cfg, seed.Options{
		Admin: seed.Admin{
			Email:    config.Getenv(os.Getenv, "ADMIN_EMAIL"),
			Name:     config.Getenv(os.Getenv, "ADMIN_NAME"),
			Password: config.Getenv(os.Getenv, "ADMIN_PASSWORD"),
		},
		WorkspaceName: *workspaceName,
		Brand:         pack,
	}, log)
}

func loadPack(file, example string) (*brand.Pack, error) {
	switch {
	case file != "" && example != "":
		return nil, errors.New("use -brand-file or -brand-example, not both")
	case file != "":
		data, err := os.ReadFile(file) // #nosec G304 -- operator-supplied path
		if err != nil {
			return nil, err
		}
		return brand.ParsePack(data)
	case example != "":
		return examples.BrandPack(example)
	}
	return nil, nil
}
