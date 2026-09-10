import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import type { GetManualChunk } from 'rollup'

// Vendor splitting. Only packages with a single, unambiguous home get a named
// chunk:
// - react is loaded by the entry either way; a separate chunk only improves
//   long-term caching.
// - recharts and xterm are imported solely by lazy pages, so their chunks
//   stay on-demand and never hit the login page. Packages listed under the
//   recharts group are its exclusive dependencies (checked against
//   node_modules/recharts/package.json); anything shared with eager code
//   (clsx, immer via zustand, use-sync-external-store, ...) is deliberately
//   left to rollup so an eager chunk never imports from a lazy one.
// antd is intentionally NOT grouped. Two attempts failed for structural
// reasons: the object form treats each listed package entry as a facade and
// drags in its whole dependency tree (~1.4 MB of antd + icons, tree-shaking
// defeated, first paint regressed), and grouping every module under
// node_modules/antd forces page-specific components (Table, DatePicker, ...)
// into an eagerly loaded chunk for the same reason — the entry imports the
// antd barrel, which statically links every component. Splitting by component
// is the only way around that and was ruled out as too aggressive, so antd
// keeps rollup's default lazy-aware placement.
const manualChunks: GetManualChunk = (id) => {
  if (!id.includes('node_modules')) return null
  if (id.includes('@xterm')) return 'xterm'
  if (
    id.includes('recharts') ||
    id.includes('victory-vendor') ||
    id.includes('@reduxjs') ||
    id.includes('react-redux') ||
    id.includes('reselect') ||
    id.includes('es-toolkit') ||
    id.includes('decimal.js-light') ||
    id.includes('eventemitter3') ||
    id.includes('tiny-invariant')
  ) {
    return 'recharts'
  }
  if (
    id.includes('/react/') ||
    id.includes('/react-dom/') ||
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
        // Without this, rollup merges every dependency of a grouped module
        // into that chunk — clsx and use-sync-external-store ended up inside
        // the lazy recharts chunk and the entry statically imported it,
        // preloading 356 kB on the login page. Explicit-only keeps shared
        // dependencies under default (eager-aware) chunking.
        onlyExplicitManualChunks: true,
      },
    },
  },
})
