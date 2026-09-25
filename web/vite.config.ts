import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const distDir = fileURLToPath(new URL('../internal/webui/dist/', import.meta.url))

// internal/webui/server.go embeds this output with `//go:embed all:dist`,
// which is a compile-time error when the pattern matches nothing. The bundle
// itself is gitignored, so on a fresh clone dist is empty and `go build ./...`
// — and therefore `make test` and lefthook's pre-push hook — fails before a
// contributor has any reason to suspect the web build. A tracked .gitkeep is
// enough to satisfy the embed without committing the bundle.
//
// emptyOutDir wipes that placeholder on every build, so restore it here rather
// than in the Makefile: this way it holds for a bare `npm run build` too, and
// the tracked file never shows up as deleted in `git status`.
function keepDistTracked(): Plugin {
  return {
    name: 'cerberus-keep-dist-tracked',
    apply: 'build',
    closeBundle() {
      writeFileSync(join(distDir, '.gitkeep'), '')
    },
  }
}

export default defineConfig({
  base: '/',
  plugins: [react(), tailwindcss(), keepDistTracked()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      // The console only accepts a mutation whose Origin exactly matches one
      // of its own loopback origins, and the dev server's origin is not one of
      // them. Present the console's origin instead. Host stays localhost:5173,
      // which the console accepts because it checks the name, not the port.
      '/api': {
        target: 'http://127.0.0.1:4783',
        configure: (proxy) => {
          proxy.on('proxyReq', (proxyReq) => {
            if (proxyReq.getHeader('origin')) {
              proxyReq.setHeader('origin', 'http://127.0.0.1:4783')
            }
          })
        },
      },
    },
  },
})
