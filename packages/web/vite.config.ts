import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5280,
    proxy: {
      // 开发模式代理到本机 daemon
      '/api': 'http://127.0.0.1:6280',
      '/mcp': 'http://127.0.0.1:6280',
    },
  },
  build: {
    outDir: 'dist',
  },
});
