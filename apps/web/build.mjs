/**
 * 前端构建入口。
 *
 * 不用 vite 的 .bin shim：在 make 的配方环境里 PATH 查找 .cmd 不可靠，
 * 直接用 node 调 vite 的 API 最稳。
 */

import { fileURLToPath } from 'node:url';
import { build } from 'vite';
import react from '@vitejs/plugin-react';

const root = fileURLToPath(new URL('.', import.meta.url));

await build({
  root,
  configFile: false,
  plugins: [react()],
  logLevel: 'warn',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 800,
  },
});
