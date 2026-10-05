import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'
import {resolve} from 'node:path'
import {readFileSync} from 'node:fs'

// Versions of the frontend libraries that actually got bundled, read from
// their installed package.json at build time, for Settings -> About. The
// installed copy, not the range in our package.json: "^6.1.0-beta" says
// little about which beta shipped.
function installedVersion(pkg: string): string {
  try {
    return JSON.parse(readFileSync(resolve(__dirname, 'node_modules', pkg, 'package.json'), 'utf8')).version
  } catch {
    return 'unknown'
  }
}
const frontendDeps = [
  ['xterm.js', '@xterm/xterm'],
  ['Svelte', 'svelte'],
  ['noVNC', '@novnc/novnc'],
  ['Vite', 'vite'],
].map(([name, pkg]) => ({name, module: pkg, version: installedVersion(pkg)}))

// Desktop app loads bundle from embedded FS - no network cost - so
// the "chunks > 500 KB" warning is noise. Bump the limit instead of
// chasing manualChunks (which complicates the Wails embed for ~zero
// user-visible win).
export default defineConfig({
  plugins: [svelte()],
  define: {
    __FRONTEND_DEPS__: JSON.stringify(frontendDeps),
  },
  server: {
    host: '127.0.0.1',
  },
  build: {
    chunkSizeWarningLimit: 2000,
    // The app only ever runs in its own embedded WebView2 (Chromium) /
    // WebKitGTK, both current. Target esnext so dependencies using
    // top-level await (noVNC 1.7's rfb core) transpile cleanly instead
    // of erroring against the default es2020 target.
    target: 'esnext',
    rollupOptions: {
      input: {
        // The desktop app.
        main: resolve(__dirname, 'index.html'),
        // The browser-share guest page, served over HTTPS by the Go share
        // server (internal/share). Standalone: it must NOT pull in the Wails
        // runtime or api.ts (they don't exist in a plain browser); it speaks a
        // single websocket. `//go:embed all:frontend/dist` picks it up.
        guest: resolve(__dirname, 'guest.html'),
      },
    },
  },
  // Match the dev-time prebundle target so `vite dev` accepts the same
  // top-level await.
  optimizeDeps: {
    esbuildOptions: {target: 'esnext'},
  },
})
