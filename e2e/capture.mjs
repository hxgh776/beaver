// 生成 docs/screenshots 的 UI 截图：沙箱 daemon + 真实页面渲染。
// 沙箱纪律与 e2e 相同——数据目录与 agent 配置根都指向临时目录。
// 用法：node capture.mjs（先 pnpm -r run build）
import { spawn } from 'node:child_process';
import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { chromium } from '@playwright/test';

const outDir = new URL('../docs/screenshots', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1');

const homeDir = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-shot-'));
const serverDist = new URL('../packages/server/dist/index.js', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1');
const port = 6291;
const child = spawn(
  process.execPath,
  [serverDist, '--home', homeDir, '--agent-home', homeDir, '--port', String(port)],
  { stdio: ['ignore', 'inherit', 'inherit'], windowsHide: true },
);

const configPath = path.join(homeDir, 'config.json');
let token = '';
for (let i = 0; i < 100; i++) {
  try {
    token = JSON.parse(await fs.readFile(configPath, 'utf8')).settings.token;
    break;
  } catch {
    await new Promise((r) => setTimeout(r, 100));
  }
}
const base = `http://127.0.0.1:${port}`;
for (let i = 0; i < 100; i++) {
  try {
    const res = await fetch(`${base}/api/status`, { headers: { authorization: `Bearer ${token}` } });
    if (res.ok) break;
  } catch {}
  await new Promise((r) => setTimeout(r, 100));
}

// 种子数据：两个 server + 一个分组，让页面呈现真实使用状态
const auth = { authorization: `Bearer ${token}`, 'content-type': 'application/json' };
await fetch(`${base}/api/servers`, {
  method: 'POST',
  headers: auth,
  body: JSON.stringify({
    name: 'filesystem',
    transport: 'stdio',
    command: 'npx',
    args: ['-y', '@modelcontextprotocol/server-filesystem', 'D:\\proj'],
    gatewayMode: true,
  }),
});
await fetch(`${base}/api/servers`, {
  method: 'POST',
  headers: auth,
  body: JSON.stringify({ name: 'context7', transport: 'http', url: 'https://mcp.context7.com/mcp', gatewayMode: true }),
});
await fetch(`${base}/api/groups`, {
  method: 'POST',
  headers: auth,
  body: JSON.stringify({ name: '常用工具', serverIds: ['filesystem', 'context7'], agentTypes: ['claude-code'] }),
});
await fetch(`${base}/api/bindings/claude-code/context7`, { method: 'PUT', headers: auth });
await fetch(`${base}/api/gateway/start`, { method: 'POST', headers: auth });

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1600, height: 900 }, reducedMotion: 'reduce' });

await page.goto(base);
await page.waitForSelector('[data-testid="home-dir"]', { timeout: 15000 });
await page.waitForTimeout(800);
await page.screenshot({ path: path.join(outDir, 'overview.png') });

await page.getByTestId('tab-servers').click();
await page.waitForTimeout(400);
await page.screenshot({ path: path.join(outDir, 'servers.png') });

await page.getByTestId('tab-discovery').click();
// 只等结果卡片（div）——搜索框 input 的 testid 也是 registry- 前缀，不能放行；无外网时等错误提示兜底
await page
  .waitForSelector('div[data-testid^="registry-"], p.text-amber-600', { timeout: 30000 })
  .catch(() => {});
await page.waitForTimeout(400);
await page.screenshot({ path: path.join(outDir, 'discovery.png') });

await page.getByTestId('tab-sync').click();
await page.waitForTimeout(500);
await page.screenshot({ path: path.join(outDir, 'sync.png') });

await page.getByTestId('tab-gateway').click();
await page.waitForTimeout(500);
await page.screenshot({ path: path.join(outDir, 'gateway.png') });

await page.getByTestId('tab-logs').click();
await page.waitForTimeout(500);
await page.screenshot({ path: path.join(outDir, 'logs.png') });

await page.getByTestId('tab-skills').click();
await page.waitForTimeout(500);
await page.screenshot({ path: path.join(outDir, 'skills.png') });

// 供应商页带一条真实档案（从预设创建 + 切换）
await fetch(`${base}/api/providers/from-preset`, {
  method: 'POST',
  headers: auth,
  body: JSON.stringify({ app: 'claude-code', presetName: 'Kimi', apiKey: 'sk-***' }),
});
const providerApps = (await (await fetch(`${base}/api/providers`, { headers: auth })).json()).apps;
const kimi = providerApps.find((a) => a.app === 'claude-code').profiles.find((p) => p.name === 'Kimi');
await fetch(`${base}/api/providers/${kimi.id}/switch`, { method: 'POST', headers: auth, body: '{}' });
await page.getByTestId('tab-providers').click();
await page.waitForTimeout(500);
await page.screenshot({ path: path.join(outDir, 'providers.png') });

await browser.close();
child.kill();
await fs.rm(homeDir, { recursive: true, force: true }).catch(() => {});
console.log('docs screenshots saved');
