-- +goose Up
-- The score flag performance_claim_risk is now restricted_claim_risk: a
-- natural reply would need a claim the brand never makes, whatever the brand.
UPDATE opportunities
SET score_breakdown = jsonb_set(score_breakdown, '{flags}',
  (SELECT COALESCE(jsonb_agg(DISTINCT CASE WHEN f = 'performance_claim_risk' THEN 'restricted_claim_risk' ELSE f END), '[]'::jsonb)
   FROM jsonb_array_elements_text(score_breakdown->'flags') AS f))
WHERE score_breakdown->'flags' ? 'performance_claim_risk';

-- +goose Down
UPDATE opportunities
SET score_breakdown = jsonb_set(score_breakdown, '{flags}',
  (SELECT COALESCE(jsonb_agg(CASE WHEN f = 'restricted_claim_risk' THEN 'performance_claim_risk' ELSE f END), '[]'::jsonb)
   FROM jsonb_array_elements_text(score_breakdown->'flags') AS f))
WHERE score_breakdown->'flags' ? 'restricted_claim_risk';
