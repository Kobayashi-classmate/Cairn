// SPDX-License-Identifier: AGPL-3.0-only
import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Address of a locally running cairn-web, used only by the dev server proxy.
const DEV_BACKEND = 'https://127.0.0.1:28443'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  define: {
    // vue-i18n compile-time feature flags: Composition API only, no devtools in production.
    __VUE_I18N_FULL_INSTALL__: true,
    __VUE_I18N_LEGACY_API__: false,
    __INTLIFY_DROP_MESSAGE_COMPILER__: false,
    __INTLIFY_PROD_DEVTOOLS__: false,
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // The tree-shaken ECharts chunk alone is just over Vite's 500 kB default.
    chunkSizeWarningLimit: 600,
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            // Only pages with charts load ECharts; a separate chunk also stays
            // cached across releases that do not upgrade it.
            { name: 'echarts', test: /[\\/]node_modules[\\/](echarts|zrender)[\\/]/ },
          ],
        },
      },
    },
  },
  server: {
    proxy: {
      '/api': {
        target: DEV_BACKEND,
        // The local backend uses a self-signed certificate.
        secure: false,
        changeOrigin: true,
        configure(proxy) {
          // The backend checks Origin on write requests; present the dev
          // server's requests as coming from the backend's own origin.
          proxy.on('proxyReq', (proxyReq) => {
            if (proxyReq.getHeader('origin')) {
              proxyReq.setHeader('origin', DEV_BACKEND)
            }
          })
        },
      },
    },
  },
})
