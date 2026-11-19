// Render docs/brand SVG sources to PNG via Playwright chromium.
// Run from repo root: node e2e/render-brand.mjs
// (playwright resolves from e2e/node_modules, mirroring capture.mjs)
import { chromium } from '@playwright/test';
import { fileURLToPath, pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const here = path.dirname(fileURLToPath(import.meta.url));
const brand = path.resolve(here, '../docs/brand');
const webPublic = path.resolve(here, '../packages/web/public');
fs.mkdirSync(webPublic, { recursive: true });

const targets = [
  // [svg, outPng, width, height, transparent, deviceScaleFactor]
  ['logo-mark.svg', path.join(brand, 'logo-mark-512.png'), 512, 512, true, 1],
  ['logo-mark.svg', path.join(webPublic, 'apple-touch-icon.png'), 180, 180, true, 1],
  ['logo-lockup.svg', path.join(brand, 'logo-lockup.png'), 460, 96, true, 2],
  ['logo-lockup-dark.svg', path.join(brand, 'logo-lockup-dark.png'), 460, 96, true, 2],
];

const browser = await chromium.launch();
for (const [svg, out, w, h, transparent, dsf] of targets) {
  const page = await browser.newPage({ viewport: { width: w, height: h }, deviceScaleFactor: dsf });
  // Standalone SVG pages render at intrinsic size — scale via an <img> wrapper.
  // The wrapper must live on the same file:// origin or the img load is blocked.
  const htmlPath = path.join(brand, `_wrap-${w}x${h}.html`);
  fs.writeFileSync(
    htmlPath,
    `<!doctype html><html><body style="margin:0;background:transparent">
       <img src="${path.basename(svg)}" width="${w}" height="${h}" style="display:block">
     </body></html>`,
  );
  await page.goto(pathToFileURL(htmlPath).href, { waitUntil: 'networkidle' });
  await page.screenshot({ path: out, omitBackground: transparent });
  await page.close();
  fs.unlinkSync(htmlPath);
  console.log('wrote', out);
}
await browser.close();
