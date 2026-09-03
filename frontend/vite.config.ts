import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

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
  // Chunking is left to rollup: with route-level lazy loading it moves each
  // page's dependencies (recharts, xterm, most of antd) into chunks that load
  // only when the page is visited. The previous manualChunks config forced
  // every visitor to download all of them upfront.
})
