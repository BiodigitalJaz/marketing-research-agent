package jobs

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// RotateSecretsArgs re-encrypts secrets sealed with a previous master key
// (RESEARCH_AGENT_SECRETS_KEY_PREVIOUS) under the current one. Periodic and safe
// to run any time; with no previous key loaded it only reports rows it
// cannot read.
type RotateSecretsArgs struct{}

func (RotateSecretsArgs) Kind() string { return "rotate_secrets" }

type RotateSecretsWorker struct {
	river.WorkerDefaults[RotateSecretsArgs]
	Deps
}

func (w *RotateSecretsWorker) Work(ctx context.Context, _ *river.Job[RotateSecretsArgs]) error {
	if !w.Secrets.Enabled() {
		return nil
	}
	q := store.New(w.Pool)
	rows, err := q.ListSecretsNotOnKey(ctx, w.Secrets.CurrentKeyID())
	if err != nil {
		return fmt.Errorf("list secrets: %w", err)
	}
	rotated, unreadable := 0, 0
	for _, s := range rows {
		plain, err := w.Secrets.Open(secrets.Sealed{Ciphertext: s.Ciphertext, Nonce: s.Nonce, KeyID: s.KeyID}, s.ID, s.WorkspaceID)
		if err != nil {
			unreadable++
			w.Log.WarnContext(ctx, "secret not readable with configured keys", "secret_id", s.ID, "key_id", s.KeyID)
			continue
		}
		sealed, err := w.Secrets.Seal(plain, s.ID, s.WorkspaceID)
		if err != nil {
			return fmt.Errorf("reseal %s: %w", s.ID, err)
		}
		if err := q.ReencryptSecret(ctx, store.ReencryptSecretParams{ID: s.ID, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, KeyID: sealed.KeyID}); err != nil {
			return fmt.Errorf("store resealed %s: %w", s.ID, err)
		}
		rotated++
	}
	if rotated > 0 || unreadable > 0 {
		w.Log.InfoContext(ctx, "secret rotation", "rotated", rotated, "unreadable", unreadable)
	}
	return nil
}
