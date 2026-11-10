import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  plugins: [tailwindcss(), sveltekit()],
  // Fonts must be served as files (CSP font-src 'self'), never inlined as data: URLs.
  build: { assetsInlineLimit: 0 },
  server: {
    // Forward API calls to the Go backend during development.
    proxy: { '/api': 'http://localhost:8080' },
  },
  test: {
    include: ['src/**/*.{test,spec}.ts'],
    environment: 'node',
  },
});
