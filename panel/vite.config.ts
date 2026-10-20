/// <reference types="vitest/config" />
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// In development the panel talks to a gateway on this address (docker compose: 8088).
const gateway = process.env["GATEWAY_URL"] ?? "http://127.0.0.1:8088";

export default defineConfig({
  plugins: [react()],
  server: { proxy: { "/admin": gateway } },
  test: { environment: "jsdom", globals: true, setupFiles: ["./src/test-setup.ts"], css: false },
});
