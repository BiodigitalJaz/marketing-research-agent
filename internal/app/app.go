// Package app holds startup plumbing shared by the cmd/ binaries.
package app

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/config"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/logging"
)

// Bootstrap parses the -config flag (default $RESEARCH_AGENT_CONFIG or
// config.yaml), loads and validates config, and builds the logger. It logs
// config warnings. On invalid config it returns an error and the caller
// must exit non-zero.
func Bootstrap(service string) (*config.Config, *slog.Logger, error) {
	def := config.Getenv(os.Getenv, "CONFIG")
	if def == "" {
		def = "config.yaml"
	}
	path := flag.String("config", def, "path to config YAML")
	flag.Parse()

	cfg, err := config.Load(*path, os.Environ)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", service, err)
	}
	log := logging.New(os.Stderr, cfg.Log.Format, cfg.Log.Level).With("service", service)
	for _, w := range cfg.Warnings() {
		log.Warn("config warning", "detail", w)
	}
	return cfg, log, nil
}

// SecureCookies reports whether cookies should be Secure, from base_url.
func SecureCookies(cfg *config.Config) bool {
	return strings.HasPrefix(strings.ToLower(cfg.Server.BaseURL), "https://")
}
