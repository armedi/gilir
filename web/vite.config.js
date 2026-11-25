import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

const API_TARGET = 'http://127.0.0.1:4718';
const DEV_PORT = 5173;

function isLoopbackHost(hostname) {
  const value = hostname.replace(/^\[|\]$/g, '').toLowerCase();
  return value === 'localhost' || value === '127.0.0.1' || value === '::1';
}

function isExactDevOrigin(origin, host) {
  if (!origin || !host) return false;
  try {
    const originUrl = new URL(origin);
    const hostUrl = new URL(`http://${host}`);
    return (
      originUrl.protocol === 'http:' &&
      originUrl.origin === hostUrl.origin &&
      hostUrl.port === String(DEV_PORT) &&
      isLoopbackHost(hostUrl.hostname)
    );
  } catch {
    return false;
  }
}

export default defineConfig({
  plugins: [solid()],
  server: {
    port: DEV_PORT,
    strictPort: true,
    proxy: {
      '/api': {
        target: API_TARGET,
        changeOrigin: true,
        configure(proxy) {
          proxy.on('proxyReq', (proxyRequest, request) => {
            if (isExactDevOrigin(request.headers.origin, request.headers.host)) {
              proxyRequest.setHeader('Origin', API_TARGET);
            }
          });
        },
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
});
