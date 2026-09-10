import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export default defineConfig({
  plugins: [svelte()],
  build: { outDir: '../pb_public', emptyOutDir: true },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8124', // PocketBase auth and admin API
      '/japi': 'http://127.0.0.1:8124', // Jackery feature API
      '/ws': { target: 'ws://127.0.0.1:8124', ws: true },
      '/_': 'http://127.0.0.1:8124',
    },
  },
});
