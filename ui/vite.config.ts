/// <reference types="vitest/config" />
import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

// The console is served by the OpenPay binary under /payments/ (see
// cmd/http_server/console.go), next to the API under /openpay/v1/.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')

  return {
    plugins: [react()],
    base: '/payments/',
    resolve: {
      dedupe: ['react', 'react-dom', '@emotion/react', '@emotion/styled', '@mui/material'],
    },
    server: {
      // Local development without opengate: proxy the API to a local
      // OpenPay. The operator headers come from the client itself
      // (VITE_DEV_USER_ID / VITE_DEV_USER_PERMS, see utils/httpClient.ts).
      proxy: env.DEV_OPENPAY_URL
        ? {
            '/openpay/v1': {
              target: env.DEV_OPENPAY_URL,
              changeOrigin: true,
            },
          }
        : undefined,
    },
    test: {
      environment: 'node',
      include: ['src/**/*.test.ts'],
    },
    build: {
      rollupOptions: {
        output: {
          manualChunks(id: string) {
            if (id.includes('node_modules/react') || id.includes('node_modules/react-dom') || id.includes('node_modules/react-router-dom')) {
              return 'react-vendor'
            }
            if (id.includes('node_modules/@mui/material') || id.includes('node_modules/@emotion')) {
              return 'mui-core'
            }
            if (id.includes('node_modules/@mui/icons-material')) {
              return 'mui-icons'
            }
            if (id.includes('node_modules/@gofreego/tsutils')) {
              return 'app-utils'
            }
          },
        },
      },
    },
  }
})
