package knowledge

import (
	"context"
	"errors"
	"hash/fnv"

	"github.com/google/uuid"
)

// ErrBusy means another sync or memory bank build holds the workspace's
// knowledge lock; the caller should try again later rather than overlap.
var ErrBusy = errors.New("a knowledge sync or memory bank build is already running for this workspace")

func lockKey(ws uuid.UUID) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("knowledge:"))
	_, _ = h.Write(ws[:])
	return int64(h.Sum64()) // #nosec G115 -- a lock key; wrap-around is fine
}

// withLock runs fn while holding the workspace's knowledge lock (a session
// advisory lock on a dedicated pool connection), so syncs and builds never
// overlap. It returns ErrBusy without waiting if the lock is taken.
func (s *Service) withLock(ctx context.Context, ws uuid.UUID, fn func() error) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockKey(ws)).Scan(&got); err != nil {
		return err
	}
	if !got {
		return ErrBusy
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockKey(ws)) }()
	return fn()
}
