import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import optimizeLocales from '@react-aria/optimize-locales-plugin';

export default defineConfig(({ mode }) => ({
  // Remote demos must use the gated same-origin proxy, regardless of shell or .env overrides.
  ...(mode === 'remote-demo' ? {
    envDir: false,
    envPrefix: [],
    define: { 'import.meta.env.VITE_API_BASE_URL': JSON.stringify('') },
  } : {}),
  // React Aria ships strings for ~34 locales; the guest UI only needs ja and zh-Hant (en-US is the fallback).
  plugins: [{ ...optimizeLocales.vite({ locales: ['ja-JP', 'zh-TW', 'en-US'] }), enforce: 'pre' }, react()],
  server: {
    host: '127.0.0.1',
    proxy: { '/v1': { target: 'http://127.0.0.1:8081', changeOrigin: true } },
  },
  test: {
    environment: 'node',
    css: { include: [/[?&]raw(?:&|$)/] },
    include: ['src/**/*.test.{ts,tsx}'],
  },
}));
