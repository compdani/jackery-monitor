import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig, loadEnv } from 'vite';
import { resolve } from 'node:path';

/**
 * Dev API proxy target.
 *
 * Default: local FastAPI on :8000.
 * Point at a real NAS / tunnel to exercise live APIs without rebuilding:
 *
 *   JACKERY_DEV_API=https://jackery.example.com npm run dev --prefix ui
 *   # or put JACKERY_DEV_API=… in ui/.env.local
 */
function proxyTarget(mode: string): { http: string; ws: string; label: string } {
  const env = loadEnv(mode, resolve(__dirname), '');
  const raw = (
    process.env.JACKERY_DEV_API ||
    env.JACKERY_DEV_API ||
    'http://127.0.0.1:8000'
  ).trim().replace(/\/+$/, '');

  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new Error(
      `JACKERY_DEV_API must be an absolute URL (got ${JSON.stringify(raw)})`,
    );
  }

  const http = url.origin;
  const wsProto = url.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = `${wsProto}//${url.host}`;
  return { http, ws, label: http };
}

export default defineConfig(({ mode }) => {
  const isProd = mode === 'production';
  const target = proxyTarget(mode);
  if (!isProd) {
    console.log(`[ui] proxying /api + /ws → ${target.label}`);
    console.log(`[ui] open http://localhost:5173/  (Kit file routes)`);
  }

  const proxyOpts = {
    target: target.http,
    changeOrigin: true,
    secure: false, // allow self-signed / LAN TLS
  };

  return {
    plugins: [tailwindcss(), sveltekit()],
    server: {
      proxy: {
        '/api': proxyOpts,
        '/ws': { ...proxyOpts, target: target.ws, ws: true },
        '/manifest.webmanifest': proxyOpts,
        '/sw.js': proxyOpts,
        // Icon + other server statics (Kit serves /_app itself in dev)
        '/static': proxyOpts,
      },
    },
  };
});
