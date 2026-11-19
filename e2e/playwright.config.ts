import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  timeout: 60_000,
  use: {
    baseURL: 'http://127.0.0.1:6290',
    headless: true,
  },
  reporter: [['list']],
  workers: 1,
});
