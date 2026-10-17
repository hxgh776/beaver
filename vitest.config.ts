import path from 'node:path';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  resolve: {
    alias: {
      // 让测试直接跑各包的 TS 源码，无需先 build
      '@beaver/core': path.resolve(__dirname, 'packages/core/src/index.ts'),
      '@beaver/server': path.resolve(__dirname, 'packages/server/src/index.ts'),
    },
  },
  test: {
    include: ['packages/*/test/**/*.test.ts'],
    testTimeout: 20000,
    hookTimeout: 20000,
    pool: 'forks',
    // 测试大量做解压/扫描等重 IO，Windows 上并发 worker 会互相放大瞬时文件锁
    fileParallelism: false,
  },
  esbuild: { target: 'es2022' },
});
