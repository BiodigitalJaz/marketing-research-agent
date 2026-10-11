-- +goose Up
-- The Reddit connector was removed. Disable any Reddit sources so monitors
-- skip them instead of failing; the rows stay for their collected items.
UPDATE sources SET enabled = false WHERE connector = 'reddit';

-- +goose Down
SELECT 1;
