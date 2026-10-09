-- These three indexes are pure duplicates of the leading column of an
-- existing UNIQUE(user_id, X) constraint on the same table (Postgres
-- already builds a btree index for a unique constraint, and a composite
-- index's leading column serves single-column lookups on that column just
-- as well — verified with EXPLAIN against a populated dev DB: dropping
-- idx_subscriptions_user_id made the planner pick
-- subscriptions_user_id_podcast_id_key instead, identical cost). They only
-- cost extra disk space and slow down every INSERT/UPDATE/DELETE on these
-- (small, frequently-written) tables for zero read benefit.
--
-- queue's idx_queue_user_id_position is NOT one of these — its second
-- column (position) differs from the UNIQUE(user_id, episode_id)
-- constraint's, and ORDER BY position after WHERE user_id = $1 genuinely
-- needs it.
DROP INDEX IF EXISTS idx_subscriptions_user_id;
DROP INDEX IF EXISTS idx_progress_user_id;
DROP INDEX IF EXISTS idx_favorites_user_id;
