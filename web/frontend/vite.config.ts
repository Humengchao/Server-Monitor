import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import type { GetManualChunk } from 'rollup'

const manualChunks: GetManualChunk = (id) => {
  // Rollup folds a chunk's unassigned static dependencies into the first
  // manual chunk that needs them. Vite's lazy-import helper is needed by
  // CodeMirror's language loader, so it landed in "codemirror" and the entry
  // page downloaded the whole editor just to lazy-load the dashboard. Pin the
  // helper, and React's small companion packages that recharts and
  // react-i18next both pull in, to the chunk every page loads.
  if (id.includes('vite/preload-helper')) return 'react'
  if (!id.includes('node_modules')) return null
  if (id.includes('@xterm')) return 'xterm'
  // Long-lived vendor chunks: a page change or a copy fix no longer invalidates
  // the 600 kB of UI kit and chart code that every deploy used to re-download.
  if (id.includes('/antd/') || id.includes('/@ant-design/') || id.includes('/rc-') || id.includes('/@rc-component/')) return 'antd'
  if (id.includes('/recharts') || id.includes('/d3-') || id.includes('/victory-vendor/')) return 'charts'
  // Core only: the language packs behind @codemirror/language-data are dynamic
  // imports, and grouping them here would load every grammar for one file.
  if (/\/(@codemirror\/(state|view|language|commands|search|autocomplete|lint|theme-one-dark|language-data)|codemirror|@lezer\/(common|highlight|lr))\//.test(id)) return 'codemirror'
  if (
    id.includes('/react/') ||
    id.includes('/react-dom/') ||
    id.includes('/react-is/') ||
    id.includes('/use-sync-external-store/') ||
    id.includes('/scheduler/') ||
    id.includes('/react-router') ||
    id.includes('/@remix-run/')
  ) {
    return 'react'
  }
  return null
}

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        // Overridable because 8080 is a popular port: a developer whose 8080 is
        // already taken had no way to run the dev server without editing this
        // file. VITE_API_PROXY=http://localhost:8099 npm run dev
        target: process.env.VITE_API_PROXY || 'http://localhost:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
  build: {
    rollupOptions: {
      output: {
        manualChunks,
      },
    },
  },
})
