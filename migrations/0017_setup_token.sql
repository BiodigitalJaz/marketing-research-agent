-- +goose Up
-- First-run setup: while no user exists, the API stores the hash of a
-- one-time setup token (printed in its log) here. Setup deletes the row.
CREATE TABLE setup_token (
  id         boolean PRIMARY KEY DEFAULT true CHECK (id),
  token_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE setup_token;
