import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  kit: {
    adapter: adapter({
      pages: '../web/next',
      assets: '../web/next',
      fallback: 'index.html',
      precompress: false,
      strict: true,
    }),
    // Root-relative /_app/* URLs (not path-relative) so the SPA shell works
    // when FastAPI serves the same index.html for /energy, /settings, etc.
    // server.py mounts web/next/_app at /_app.
    paths: {
      relative: false,
    },
    prerender: {
      // app.html links to FastAPI-only paths (/manifest.webmanifest, /static/…).
      handleHttpError: 'warn',
    },
    alias: {
      '@/*': './src/lib/*',
      $lib: './src/lib',
    },
  },
};

export default config;
