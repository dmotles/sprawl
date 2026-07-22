// Formatting helpers for host-token metadata. int64 proto fields arrive as
// bigint (protobuf-es v1); 0n is the "unset" sentinel for the revoked timestamp.

// formatUnixMs renders a bigint epoch-millis value as a stable ISO-8601 string,
// or an em dash for the 0n sentinel. ISO (UTC) is used rather than a locale
// string so the output is deterministic across environments.
export function formatUnixMs(ms: bigint): string {
  if (ms === 0n) return "—";
  return new Date(Number(ms)).toISOString();
}

// isActive reports whether a token is still active (never revoked).
export function isActive(t: { revokedAtUnixMs: bigint }): boolean {
  return t.revokedAtUnixMs === 0n;
}
