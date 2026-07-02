import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'

// Desktop app loads bundle from embedded FS - no network cost - so
// the "chunks > 500 KB" warning is noise. Bump the limit instead of
// chasing manualChunks (which complicates the Wails embed for ~zero
// user-visible win).
export default defineConfig({
  plugins: [svelte()],
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
  },
  // Match the dev-time prebundle target so `vite dev` accepts the same
  // top-level await.
  optimizeDeps: {
    esbuildOptions: {target: 'esnext'},
  },
})
