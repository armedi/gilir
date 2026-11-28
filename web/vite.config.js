import { defineConfig, lazyPlugins } from 'vite-plus';
import solid from '@solidjs/vite-plugin';

const API_TARGET = 'http://127.0.0.1:44547';
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
  plugins: lazyPlugins(() => [solid()]),
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
    // The production CSP allows font URLs from this origin, not data URLs.
    assetsInlineLimit: 0,
  },
});
