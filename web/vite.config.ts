import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    // In development the SPA runs on :5173 and the api service on :8092.
    // Proxying /api makes the browser see ONE origin, exactly like the cloud
    // Ingress and the Compose proxy do (so the app never needs CORS).
    // Override with API_PROXY_TARGET if the api runs elsewhere.
    proxy: {
      '/api': process.env.API_PROXY_TARGET ?? 'http://localhost:8092',
    },
  },
})
