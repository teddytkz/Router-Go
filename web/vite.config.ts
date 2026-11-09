import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// In dev the Go server (make dev) listens on :20128; the SPA proxies API calls to it.
// dev proxy target: `ROUTER_GO_PORT=8080 bun run dev` when the Go server runs on another port
const api = `http://localhost:${process.env.ROUTER_GO_PORT ?? 20128}`;

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: { port: 5173, proxy: { "/api": api, "/v1": api } },
  build: { outDir: "dist", emptyOutDir: true, chunkSizeWarningLimit: 800 },
});
