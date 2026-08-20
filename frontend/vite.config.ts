import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
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
