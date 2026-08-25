-- QUM-1251 (M2): the columns agent cards need to be DATA rather than Go
-- constants.
--
-- Appendix A's agent_cards carries the definition itself — prompt, tools, model,
-- effort, rubric, budgets — and nothing that lets a reader find the right row or
-- tell whether it still matches the binary. These three columns are exactly that
-- missing half, and no more:
--
--   agent_type     which role the card defines. Cards are identified by
--                  (name, version), so nothing in Appendix A answers "which card
--                  does a spawning engineer get". The spawn path needs it, and
--                  `sprawl def list` groups on it.
--   description    the one-line summary `sprawl def list` renders. Without it
--                  the list is a table of uuids and version numbers.
--   content_sha256 the hash of the SOURCE BYTES the card was parsed from. This
--                  is the column that makes "immutable versioned definitions"
--                  checkable rather than aspirational: the seed sync reads it
--                  back and refuses a row that has drifted from the embedded
--                  card. See syncSeedCards.
--
-- ADDITIVE AND NULLABLE, deliberately. `agent_cards` may already hold rows on an
-- upgraded database, and `ADD COLUMN ... NOT NULL` without a default fails
-- outright against a populated table while passing every test that runs against
-- a fresh schema — the failure would land only on a real deployment. Nullable is
-- also honest about what these are: a card published before M2 genuinely has no
-- content hash, and backfilling one would be inventing provenance.
--
-- No new GRANT is needed: 00002 grants SELECT on agent_cards at TABLE level, and
-- a table-level grant covers columns added later. That is asserted, not assumed
-- — see TestAgentCards_AppRoleGrantCatalogIsExactlySelect.

-- +goose Up

ALTER TABLE agent_cards ADD COLUMN agent_type     text;
ALTER TABLE agent_cards ADD COLUMN description    text;
ALTER TABLE agent_cards ADD COLUMN content_sha256 text;

-- Resolving "the card for this agent type" is the hot read on this table: every
-- spawn does it. UNIQUE is deliberately NOT used — multiple versions of a card
-- share an agent_type, which is the entire point of versioning.
CREATE INDEX agent_cards_agent_type_idx ON agent_cards (agent_type);

-- +goose Down

DROP INDEX agent_cards_agent_type_idx;
ALTER TABLE agent_cards DROP COLUMN content_sha256;
ALTER TABLE agent_cards DROP COLUMN description;
ALTER TABLE agent_cards DROP COLUMN agent_type;
