import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Dev: proxy ke server lokal. Produksi: hasil build di-embed ke biner smscd (server/internal/webui/dist).
export default defineConfig({
  plugins: [react()],
  server: { proxy: { '/api': 'http://127.0.0.1:8080', '/ws': { target: 'ws://127.0.0.1:8080', ws: true } } },
  build: { outDir: 'dist', sourcemap: false, chunkSizeWarningLimit: 400 },
  test: { environment: 'node' },
})
