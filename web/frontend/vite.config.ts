import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import type { GetManualChunk } from 'rollup'

const manualChunks: GetManualChunk = (id) => {
  if (!id.includes('node_modules')) return null
  if (id.includes('@xterm')) return 'xterm'
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
      },
    },
  },
})
