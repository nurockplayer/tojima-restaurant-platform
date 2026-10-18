import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig(({ mode }) => ({
  // Remote demos must use the gated same-origin proxy, regardless of shell or .env overrides.
  ...(mode === 'remote-demo' ? {
    envDir: false,
    envPrefix: [],
    define: { 'import.meta.env.VITE_API_BASE_URL': JSON.stringify('') },
  } : {}),
  plugins: [react()],
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
