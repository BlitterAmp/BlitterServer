import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'
import { svelteTesting } from '@testing-library/svelte/vite'

// Served by BlitterServer at /admin/ via go:embed of dist/.
export default defineConfig({
  base: '/admin/',
  plugins: [svelte(), tailwindcss(), svelteTesting()],
  test: {
    environment: 'jsdom',
  },
  server: {
    // Dev convenience only: proxy the admin API to a locally running server.
    proxy: { '/admin/api': 'http://127.0.0.1:8484', '/v1': 'http://127.0.0.1:8484' },
  },
})
