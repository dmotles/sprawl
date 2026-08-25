-- QUM-1251 (M2): the render options a card needs in order to render the same
-- way from the database as it does from the embedded seed.
--
-- `card.RenderOpts` gates three splices in `(*Card).Render`: the sub-agent
-- banner, the trailing "# Environment" block, and the TEST SANDBOX MODE warning.
-- Appendix A's agent_cards has no column for them, so a card read back from
-- Postgres carried ZERO-VALUED opts and rendered without all three — silently,
-- because the prompt still renders and still looks plausible, and because every
-- prompt-safety scanner in internal/agent reads the embedded seeds and never
-- looks at the database.
--
-- Dropping the sub-agent banner is the sharp end of that: a sub-agent would not
-- be told it shares its parent's worktree and branch.
--
-- One jsonb column rather than three booleans. The set of render options is
-- expected to grow (Appendix B's slim cards add more), and a jsonb object grows
-- without a migration per flag. The names inside it are the SAME names the seed
-- `.md` frontmatter uses — see card.RenderOpts, whose yaml and json tags are
-- deliberately identical — so an operator reading a card in either place reads
-- one vocabulary.
--
-- NULLABLE, for the reason 00003 gives at length: this table may already hold
-- rows, and `ADD COLUMN ... NOT NULL` without a default fails against a
-- populated table while passing every test that runs against a fresh schema, so
-- the failure would land only on a real deployment.
--
-- A NULL is therefore reachable, and the two sides treat it differently on
-- purpose:
--   * syncSeedCards BACKFILLS it for the cards this build ships. It writes only
--     where the column IS NULL, so it completes a row that predates this
--     migration rather than changing one that was published with a value — the
--     drift check still refuses every other divergence.
--   * CardForType REFUSES it, along with `{}` and any partially-specified
--     object. Under a struct decode an absent key and an explicit `false` are
--     the same value, so requiring all keys present is the only way to tell a
--     card that MEANS all-false from one nobody ever specified. The caller falls
--     back to the compiled-in seed, which costs a spawn its published card and
--     never its launch.
--
-- No new GRANT: 00002 grants SELECT on agent_cards at TABLE level, which covers
-- columns added later. Asserted, not assumed — see
-- TestAgentCards_AppRoleGrantCatalogIsExactlySelect and
-- TestCardForType_WorksAsTheAppRole.

-- +goose Up

ALTER TABLE agent_cards ADD COLUMN render jsonb;

-- +goose Down

ALTER TABLE agent_cards DROP COLUMN render;
