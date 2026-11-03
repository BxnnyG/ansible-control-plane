import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  kit: {
    // SPA mode: every unknown path falls back to index.html (embedded in the Go binary).
    adapter: adapter({ pages: 'build', assets: 'build', fallback: 'index.html', strict: false }),
  },
};

export default config;
