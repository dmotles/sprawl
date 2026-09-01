-- QUM-1252 (M3a): events.follows_event_id — the rework/continuation link.
--
-- The design (docs/designs/v2-log-centric-rearchitecture.md, "Event contracts")
-- specifies it and M1a did not ship it: until M3a there was no producer, so a
-- column would have been an unwritten column. It is needed now because
-- REWORK_REQUESTED is the ONLY way a defect found after a close is expressed —
-- closes are final and the log is monotone — and a rework contract that does not
-- name the contract it follows is indistinguishable from an unrelated new goal.
--
-- Distinct from closes_event_id, and the distinction is the whole point:
--
--   closes_event_id  — "this event ENDS that contract".   Maintained by the
--                      appender against open_contracts; exactly one close per
--                      opener, enforced by the RowsAffected() != 1 check.
--   follows_event_id — "this contract CONTINUES that one". Zero effect on
--                      open_contracts. Many events may follow one predecessor
--                      (a goal reworked twice), and the predecessor is normally
--                      already CLOSED — which is exactly why it cannot be
--                      expressed as a close.
--
-- REFERENCES events(id) rather than being a bare uuid: an unresolvable
-- predecessor is a broken chain, and the failure of a bare uuid is that the
-- chain reads fine for months and then dead-ends when someone walks it.
--
-- NOT NULL is deliberately absent: the overwhelming majority of events follow
-- nothing.

-- +goose Up

ALTER TABLE events ADD COLUMN follows_event_id uuid REFERENCES events(id);

-- The read this column exists to serve is "what followed X" — walking a chain
-- forward from a closed goal. Without an index that is a full scan of the log
-- per hop, and the whole point of the chain is that it is walked.
--
-- Partial, on IS NOT NULL: almost every row is NULL, so a full index would be
-- mostly a list of NULLs.
CREATE INDEX events_follows_event_id_idx ON events (follows_event_id)
    WHERE follows_event_id IS NOT NULL;

-- +goose Down

DROP INDEX events_follows_event_id_idx;
ALTER TABLE events DROP COLUMN follows_event_id;
