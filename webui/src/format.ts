/** Display helpers shared by the views. */

/** What every unknown value renders as. Never a 0, never an empty cell. */
export const UNKNOWN = "—";

/**
 * A UTC timestamp, second precision. UTC rather than the browser's zone: the
 * ledger is a distributed log read by people comparing it against server logs,
 * and a local-time rendering silently shifts every row.
 */
export function formatTime(iso: string): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return iso;
  return at
    .toISOString()
    .replace("T", " ")
    .replace(/\.\d+Z$/, "Z");
}

/**
 * Coarse "how long ago", for the fleet view's liveness column.
 *
 * Coarse on purpose: there is no liveness boolean in the log — turn boundaries
 * are the only signal, and a long quiet turn is legitimate — so this hands the
 * reader an age and lets them judge, rather than dressing a threshold up as a
 * status. A future timestamp reads "just now" rather than a negative age: two
 * hosts' clocks disagree, and that is not worth rendering as an anomaly.
 */
export function formatRelative(iso: string, now: Date = new Date()): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return iso;
  const seconds = Math.floor((now.getTime() - at.getTime()) / 1000);
  if (seconds < 60) return "just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

/** Money, to cents. `null` is unknown and must not render as $0.00. */
export function formatUsd(value: number | null | undefined): string {
  if (value === null || value === undefined) return UNKNOWN;
  return `$${value.toFixed(2)}`;
}

/** A count with thousands separators. `null` is unknown, not zero. */
export function formatCount(value: number | null | undefined): string {
  if (value === null || value === undefined) return UNKNOWN;
  return value.toLocaleString("en-US");
}

/**
 * The server-derived project label. It is the last path segment of the repo
 * remote — the raw remote URL never reaches the browser — and it can be empty
 * when a remote has no repo path.
 *
 * Absent is accepted as well as empty, because this is the JSON parse boundary:
 * an endpoint that omits the field entirely would otherwise render an *empty*
 * cell, which is exactly the silent blank the em dash exists to prevent.
 */
export function projectLabel(name: string | null | undefined): string {
  return name ? name : UNKNOWN;
}

/**
 * First segment of a uuid — enough to correlate, short enough to scan. Absent
 * or empty renders the em dash, for the same boundary reason as `projectLabel`.
 */
export function shortId(id: string | null | undefined): string {
  return id ? id.split("-")[0] : UNKNOWN;
}
