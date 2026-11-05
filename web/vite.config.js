import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// The build output is embedded into the Go binary (internal/webui/dist).
// `npm run dev` proxies the API to a bsdswitchd started with
// `bsdswitchd -sim -listen-http 127.0.0.1:8080`.
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    assetsInlineLimit: 0,
    chunkSizeWarningLimit: 600,
  },
  server: {
    proxy: { '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false } },
  },
})
