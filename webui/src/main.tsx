import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "./App";
import { ApiProvider } from "./api/ApiProvider";
import { createApiClient } from "./api/client";
import "./styles/tokens.css";
import "./styles/app.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <BrowserRouter>
      <ApiProvider client={createApiClient()}>
        <App />
      </ApiProvider>
    </BrowserRouter>
  </StrictMode>,
);
