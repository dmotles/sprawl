import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  Code,
  ConnectError,
  createPromiseClient,
  createRouterTransport,
} from "@connectrpc/connect";
import { HubService } from "../gen/hub/v1/hub_connect";
import { App } from "./App";

function authedClient() {
  let n = 0;
  const transport = createRouterTransport(({ service }) => {
    service(HubService, {
      listInstances() {
        return { instances: [] };
      },
      listHostTokens() {
        return { tokens: [] };
      },
      createHostToken() {
        n += 1;
        const tokenId = `id-${n}`;
        return { token: `sprawl_hub_${tokenId}_secret${n}`, tokenId };
      },
      revokeHostToken() {
        return {};
      },
    });
  });
  return createPromiseClient(HubService, transport);
}

function unauthedClient() {
  const transport = createRouterTransport(({ service }) => {
    service(HubService, {
      listInstances() {
        throw new ConnectError("nope", Code.Unauthenticated);
      },
    });
  });
  return createPromiseClient(HubService, transport);
}

describe("App", () => {
  it("shows the login form when unauthenticated", async () => {
    render(<App client={unauthedClient()} />);
    expect(await screen.findByLabelText(/login token/i)).toBeInTheDocument();
  });

  it("renders nav with Instances default and a Tokens view", async () => {
    const user = userEvent.setup();
    render(<App client={authedClient()} />);

    expect(
      await screen.findByRole("button", { name: /instances/i }),
    ).toBeInTheDocument();
    const tokensNav = screen.getByRole("button", { name: /tokens/i });

    await user.click(tokensNav);
    expect(await screen.findByLabelText(/label/i)).toBeInTheDocument();
  });

  it("drops the once-shown plaintext when navigating away and back", async () => {
    const user = userEvent.setup();
    render(<App client={authedClient()} />);

    await user.click(await screen.findByRole("button", { name: /tokens/i }));
    await user.type(await screen.findByLabelText(/label/i), "laptop");
    await user.click(screen.getByRole("button", { name: /create token/i }));
    expect(await screen.findByText(/sprawl_hub_id-1_secret1/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /instances/i }));
    await user.click(screen.getByRole("button", { name: /tokens/i }));

    // TokensView remounted → plaintext state dropped, never re-shown.
    expect(screen.queryByText(/sprawl_hub_id-1_secret1/)).not.toBeInTheDocument();
  });
});
