// Package store holds sqlc-generated query code (*.gen.go, regenerate with
// `make sqlc`) plus hand-written repository helpers. Every query touching
// workspace data must take and filter by workspace_id.
package store
