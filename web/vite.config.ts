import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  server: {
    host: "127.0.0.1",
    proxy: { "/v1": "http://127.0.0.1:8080" },
  },
  test: { environment: "jsdom", setupFiles: "./src/test/setup.ts" },
});
