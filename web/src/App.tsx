import { useEffect, useState } from "react";
import { Code, ConnectError, createPromiseClient } from "@connectrpc/connect";
import { HubService } from "../gen/hub/v1/hub_connect";
import type { Instance } from "../gen/hub/v1/hub_pb";
import { transport } from "./transport";

const client = createPromiseClient(HubService, transport);

type State =
  | { kind: "loading" }
  | { kind: "authed"; instances: Instance[] }
  | { kind: "unauthed" }
  | { kind: "error"; message: string };

export function App() {
  const [state, setState] = useState<State>({ kind: "loading" });

  useEffect(() => {
    let cancelled = false;
    client
      .listInstances({})
      .then((resp) => {
        if (!cancelled) setState({ kind: "authed", instances: resp.instances });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        // Unauthenticated → the cookie is missing/expired/invalid → show login.
        if (err instanceof ConnectError && err.code === Code.Unauthenticated) {
          setState({ kind: "unauthed" });
          return;
        }
        setState({ kind: "error", message: ConnectError.from(err).message });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (state.kind === "loading") return <main>Loading…</main>;
  if (state.kind === "unauthed") return <LoginForm />;
  if (state.kind === "error") {
    return (
      <main>
        <p>Error: {state.message}</p>
        <LoginForm />
      </main>
    );
  }
  return <InstanceList instances={state.instances} />;
}

// LoginForm posts the token in the request BODY (never the URL) to /login. The
// server sets the HttpOnly cookie and redirects to /app/; the SPA then loads
// authenticated. No token is ever stored in JS.
function LoginForm() {
  return (
    <main>
      <h1>sprawl hub</h1>
      <form method="post" action="/login">
        <label>
          Login token:{" "}
          <input type="password" name="token" autoComplete="off" />
        </label>{" "}
        <button type="submit">Log in</button>
      </form>
    </main>
  );
}

function InstanceList({ instances }: { instances: Instance[] }) {
  return (
    <main>
      <header>
        <h1>sprawl hub — instances</h1>
        <form method="post" action="/logout">
          <button type="submit">Log out</button>
        </form>
      </header>
      {instances.length === 0 ? (
        <p>No instances registered.</p>
      ) : (
        <ul>
          {instances.map((i) => (
            <li key={i.hostId}>
              <code>{i.hostId}</code> — {i.repoLabel || "(no repo)"}
              {i.active ? " · active" : ""}
            </li>
          ))}
        </ul>
      )}
    </main>
  );
}
