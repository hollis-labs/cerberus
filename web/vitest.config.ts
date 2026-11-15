import { defineConfig } from "vitest/config"

// Component tests run in jsdom. They import pages and the typed client; fetch is stubbed per test,
// so no test reaches a server.
export default defineConfig({
  esbuild: { jsx: "automatic" },
  test: { environment: "jsdom", include: ["src/**/*.test.tsx", "src/**/*.test.ts"] },
})
