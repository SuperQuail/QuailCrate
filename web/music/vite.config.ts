import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

// Remove crossorigin attributes on same-origin packaged assets so WebView2 virtual-origin
// resource interception (AddWebResourceRequestedFilter) never hits spurious CORS checks.
function stripCrossOrigin(): Plugin {
  return {
    name: 'quailcrate-strip-crossorigin',
    transformIndexHtml(html) {
      return html.replace(/\s+crossorigin(?:="[^"]*")?/g, '');
    },
  };
}

export default defineConfig({
  base: './',
  plugins: [react(), stripCrossOrigin()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
  build: {
    outDir: path.resolve(__dirname, '../../plugins/music/web'),
    emptyOutDir: true,
    sourcemap: false,
    cssCodeSplit: false,
    assetsInlineLimit: 0,
    rollupOptions: {
      output: {
        entryFileNames: 'assets/app.js',
        chunkFileNames: 'assets/[name].js',
        assetFileNames: (assetInfo) => {
          if (assetInfo.names?.some((n) => n.endsWith('.css')) || assetInfo.name?.endsWith('.css')) {
            return 'assets/app.css';
          }
          return 'assets/[name][extname]';
        },
      },
    },
  },
});
