import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// 控制台由 Go 服务同源托管，产物用相对根路径即可，无需 CDN 前缀。
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
});
