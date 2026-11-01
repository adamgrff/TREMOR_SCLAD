import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  envDir: process.env.TREMOR_SKIP_DOTENV === '1' ? false : undefined,
  server: {
    headers: process.env.TREMOR_TEST_CSP === '1' ? {
      // Fail closed if the explicit isolated-test API is missing or invalid.
      'Content-Security-Policy': `connect-src 'self' ${new URL(process.env.VITE_API_BASE_URL!).origin} ws://127.0.0.1:5174`,
    } : undefined,
  },
})
