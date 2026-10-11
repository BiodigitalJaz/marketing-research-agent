SHELL := /bin/bash
.SHELLFLAGS := -eo pipefail -c
GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)
CONFIG ?= config.yaml
COMPOSE := docker compose
# Deployment settings. Put your own (for example an overlay kept outside the
# repo) in a gitignored .deploy.env: K8S_OVERLAY, SEED_JOB, DEPLOY_NAMESPACE,
# DEPLOY_URL. See docs/runbook.md.
-include .deploy.env
K8S_OVERLAY ?= deploy/k8s/overlays/example-nodeport
SEED_JOB ?= deploy/k8s/jobs/seed.yaml
DEPLOY_NAMESPACE ?= research-agent
DEPLOY_URL ?=

.PHONY: help dev down logs migrate seed sqlc sqlc-check build test test-short lint lint-go lint-web check-licenses deploy-plan deploy-verify \
        check-no-posting web-build api-client tools k8s-render k8s-validate deploy seed-cluster

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(firstword $(MAKEFILE_LIST)) | awk 'BEGIN{FS=":.*?## "}{printf "  %-18s %s\n",$$1,$$2}'

dev: ## Start postgres, migrate, api, worker, web (docker compose)
	$(COMPOSE) --profile tools build seed
	$(COMPOSE) up --build -d
	@echo "web: http://localhost:3000  api: http://localhost:8080/health"

down: ## Stop the dev stack
	$(COMPOSE) down

logs: ## Tail dev stack logs
	$(COMPOSE) logs -f

migrate: ## Apply goose + River migrations (uses $(CONFIG); falls back to config.example.yaml)
	go run ./cmd/migrate -config $(if $(wildcard $(CONFIG)),$(CONFIG),config.example.yaml)

seed: ## Seed workspace, admin (RESEARCH_AGENT_ADMIN_EMAIL/PASSWORD), lane configs, routes, draft brand KB
	go run ./cmd/seed -config $(if $(wildcard $(CONFIG)),$(CONFIG),config.example.yaml)

sqlc: ## Regenerate query code from internal/store/queries
	sqlc generate

sqlc-check: ## Fail if generated sqlc code is stale
	sqlc diff

build: ## Build Go binaries into ./bin
	go build -o bin/ ./cmd/...

test: ## Go unit + integration tests (needs Docker for testcontainers)
	go test -race ./...
	scripts/check_no_posting_test.sh

test-short: ## Unit tests only (no Docker)
	go test -short ./...

lint: lint-go lint-web check-no-posting ## All linters

# staticcheck runs under the go.mod Go version: a newer local toolchain can
# emit export data that a staticcheck release does not read yet.
lint-go:
	go vet ./...
	GOTOOLCHAIN=go$$(go list -m -f '{{.GoVersion}}') staticcheck ./...
	golangci-lint run ./...

check-licenses: ## Fail on dependency licenses incompatible with AGPL-3.0-only
	scripts/check_licenses.sh

lint-web:
	cd web && pnpm lint

check-no-posting: ## Fail if connector code contains HTTP write methods
	scripts/check_no_posting.sh

web-build:
	cd web && pnpm build

api-client: ## Regenerate the TypeScript API client from openapi.yaml
	cd web && pnpm gen:api

k8s-render: ## Render Kubernetes manifests for $(K8S_OVERLAY)
	kubectl kustomize $(K8S_OVERLAY)

k8s-validate: ## Render and schema-validate Kubernetes manifests (fails on render errors or empty output)
	scripts/k8s_validate.sh $(K8S_OVERLAY)

# Image tag to deploy. Defaults to the image built for origin/main's HEAD
# (images.yml tags every main build sha-<7 chars>). Pinning a commit tag
# makes rollouts pull the new image; the mutable :main tag would be served
# from the node cache.
TAG ?= sha-$(shell git rev-parse --short=7 origin/main)
PIN_TAG = sed -E 's\#(image: [^ ]+-(api|worker|migrate|web)):main\#\1:$(TAG)\#g'

deploy-plan: ## Plan a deploy of TAG: render, check images exist, server dry run, kubectl diff (changes nothing)
	scripts/deploy_plan.sh $(K8S_OVERLAY) $(TAG)

deploy: ## Apply the plan made by deploy-plan for TAG, then wait for rollout
	@plan="$$(ls .deploy/plan-*-$(TAG).yaml 2>/dev/null | head -1 || true)"; \
	if [ -z "$$plan" ]; then echo "No plan for $(TAG). Run: make deploy-plan TAG=$(TAG)"; exit 1; fi; \
	scripts/deploy_apply.sh "$$plan"

deploy-verify: ## Verify DEPLOY_NAMESPACE runs TAG and is healthy (needs DEPLOY_URL)
	@test -n "$(DEPLOY_URL)" || { echo "Set DEPLOY_URL (the web address) in .deploy.env or on the command line"; exit 1; }
	scripts/deploy_verify.sh $(DEPLOY_NAMESPACE) $(TAG) $(DEPLOY_URL)

seed-cluster: ## Run the idempotent seed Job in DEPLOY_NAMESPACE (needs secret research-agent-admin)
	kubectl -n $(DEPLOY_NAMESPACE) delete job seed --ignore-not-found
	$(PIN_TAG) $(SEED_JOB) | sed -E 's/^(  namespace: ).*/\1$(DEPLOY_NAMESPACE)/' | kubectl apply -f -
	kubectl -n $(DEPLOY_NAMESPACE) wait --for=condition=complete job/seed --timeout=300s

tools: ## Install Go dev tools
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
	go install github.com/pressly/goose/v3/cmd/goose@v3.28.0
	go install honnef.co/go/tools/cmd/staticcheck@2026.2.1
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
	go install github.com/yannh/kubeconform/cmd/kubeconform@v0.8.0
	go install github.com/google/go-licenses/v2@v2.0.1
