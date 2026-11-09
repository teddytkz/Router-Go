import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import App from "./App";
import { ToastProvider } from "./components/ui";
import "./index.css";

const qc = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, staleTime: 5_000 } } });
// A 401 anywhere means the session ended: refetch auth status so the router sends the user to /login.
window.addEventListener("auth:expired", () => qc.invalidateQueries({ queryKey: ["auth"] }));

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <BrowserRouter><App /></BrowserRouter>
      </ToastProvider>
    </QueryClientProvider>
  </StrictMode>,
);
