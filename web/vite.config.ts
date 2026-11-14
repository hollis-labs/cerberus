import { writeFileSync } from "node:fs"
import { resolve } from "node:path"
import tailwindcss from "@tailwindcss/vite"
import { defineConfig } from "vite"

const outDir = resolve(import.meta.dirname, "../internal/webui/dist")
// The dev proxy target: the new process's loopback listener (port plan section 1). The backend
// must list the dev server in identity.allowed_origins, or writes are refused with bad_origin.
const backend = process.env.CERBERUS_DEV_BACKEND ?? "http://127.0.0.1:4786"

export default defineConfig({
  base: "/",
  plugins: [
    tailwindcss(),
    {
      // emptyOutDir removes dist/.gitkeep, which the Go embed needs to compile when unbuilt.
      name: "keep-gitkeep",
      closeBundle() {
        writeFileSync(resolve(outDir, ".gitkeep"), "")
      },
    },
  ],
  esbuild: { jsx: "automatic" },
  // app-shell ships TypeScript source; the dependency optimizer needs the same JSX runtime.
  optimizeDeps: { esbuildOptions: { jsx: "automatic" } },
  build: { outDir, emptyOutDir: true },
  server: { proxy: { "/api": backend } },
})
