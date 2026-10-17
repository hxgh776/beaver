// 导出品牌 PNG：SVG → PNG（透明角），用 e2e 自带的 Playwright Chromium 渲染。
// 用法：node e2e/export-brand.mjs
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { chromium } from '@playwright/test';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const jobs = [
  { svg: 'docs/brand/logo-mark.svg', out: 'docs/brand/logo-mark-512.png', size: 512 },
  { svg: 'docs/brand/logo-mark.svg', out: 'packages/web/public/apple-touch-icon.png', size: 180 },
  { svg: 'docs/brand/logo-lockup.svg', out: 'docs/brand/logo-lockup.png', size: 920, ratio: 96 / 460 },
  { svg: 'docs/brand/logo-lockup-dark.svg', out: 'docs/brand/logo-lockup-dark.png', size: 920, ratio: 96 / 460 },
];

const browser = await chromium.launch();
for (const job of jobs) {
  const w = job.size;
  const h = job.ratio ? Math.round(job.size * job.ratio) : job.size;
  const page = await browser.newPage({ viewport: { width: w, height: h }, deviceScaleFactor: 1 });
  await page.goto(pathToFileURL(path.join(root, job.svg)).href);
  await page.evaluate(([sw, sh]) => {
    const svg = document.querySelector('svg');
    svg.setAttribute('width', String(sw));
    svg.setAttribute('height', String(sh));
  }, [w, h]);
  await page.locator('svg').screenshot({ path: path.join(root, job.out), omitBackground: true });
  await page.close();
  console.log('✓', job.out, `${w}x${h}`);
}
await browser.close();
