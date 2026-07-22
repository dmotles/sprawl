import { useCallback, useEffect, useState } from "react";
import { ConnectError } from "@connectrpc/connect";
import type { HostToken } from "../gen/hub/v1/hub_pb";
import type { HubClient } from "./client";
import { formatUnixMs, isActive } from "./format";
import { copyText } from "./clipboard";

// TokensView is the host-token administration screen: create (plaintext shown
// ONCE, copyable, never re-shown or persisted), list (id/label/created/status),
// and revoke (with an inline confirm). The once-only plaintext lives ONLY in
// component state (`created`) — never localStorage, sessionStorage, or the URL —
// so it vanishes on dismiss, navigation (unmount), or refresh.
export function TokensView({ client }: { client: HubClient }) {
  const [label, setLabel] = useState("");
  const [created, setCreated] = useState<{ token: string; tokenId: string } | null>(
    null,
  );
  // null = not yet attempted; true = copied; false = copy failed (e.g. no
  // clipboard API over plain http://) → offer manual selection.
  const [copyOk, setCopyOk] = useState<boolean | null>(null);
  const [tokens, setTokens] = useState<HostToken[]>([]);
  const [listError, setListError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [confirmingId, setConfirmingId] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const resp = await client.listHostTokens({});
      setTokens(resp.tokens);
      setListError(null);
    } catch (err) {
      setListError(ConnectError.from(err).message);
    }
  }, [client]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    setActionError(null);
    const trimmed = label.trim();
    try {
      const resp = await client.createHostToken({ label: trimmed });
      setCreated({ token: resp.token, tokenId: resp.tokenId });
      setCopyOk(null);
      setLabel("");
      await refresh();
    } catch (err) {
      setActionError(ConnectError.from(err).message);
    }
  }

  async function onCopy() {
    if (!created) return;
    setCopyOk(await copyText(created.token));
  }

  async function onRevoke(tokenId: string) {
    setActionError(null);
    setConfirmingId(null);
    try {
      await client.revokeHostToken({ tokenId });
      await refresh();
    } catch (err) {
      setActionError(ConnectError.from(err).message);
    }
  }

  return (
    <section>
      <h2>Host tokens</h2>

      <form onSubmit={onCreate}>
        <label>
          Label:{" "}
          <input
            type="text"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            autoComplete="off"
          />
        </label>{" "}
        <button type="submit" disabled={label.trim() === ""}>
          Create token
        </button>
      </form>

      {created && (
        <div role="alert">
          <p>
            <strong>Copy this token now — it will not be shown again.</strong>
          </p>
          <code>{created.token}</code>{" "}
          <button type="button" onClick={onCopy}>
            Copy
          </button>{" "}
          <button type="button" onClick={() => setCreated(null)}>
            Dismiss
          </button>
          {copyOk === true && <span> Copied ✓</span>}
          {copyOk === false && (
            <span> Copy failed — select the token above and copy it manually.</span>
          )}
        </div>
      )}

      {actionError && <p role="alert">Error: {actionError}</p>}
      {listError && <p role="alert">Error: {listError}</p>}

      {tokens.length === 0 ? (
        <p>No tokens.</p>
      ) : (
        <ul aria-label="tokens">
          {tokens.map((t) => {
            const active = isActive(t);
            return (
              <li key={t.tokenId}>
                <code>{t.tokenId}</code> — <span>{t.label || "(no label)"}</span>{" "}
                · created {formatUnixMs(t.createdAtUnixMs)} ·{" "}
                {active ? (
                  <span>Active</span>
                ) : (
                  <span>Revoked {formatUnixMs(t.revokedAtUnixMs)}</span>
                )}
                {active &&
                  (confirmingId === t.tokenId ? (
                    <>
                      {" "}
                      <span>Revoke this token?</span>{" "}
                      <button type="button" onClick={() => onRevoke(t.tokenId)}>
                        Confirm
                      </button>{" "}
                      <button type="button" onClick={() => setConfirmingId(null)}>
                        Cancel
                      </button>
                    </>
                  ) : (
                    <>
                      {" "}
                      <button
                        type="button"
                        onClick={() => setConfirmingId(t.tokenId)}
                      >
                        Revoke
                      </button>
                    </>
                  ))}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
