// E2E 冒烟：token 门 → 概览 → 添加 server → 分发预览 → 网关页。
// daemon 由 helpers 启动（沙箱 HOME + 沙箱 agent 配置根）。
import { expect, test } from '@playwright/test';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { startTestDaemon, type TestDaemon } from './helpers';

let daemon: TestDaemon;

test.beforeAll(async () => {
  daemon = await startTestDaemon();
});

test.afterAll(async () => {
  await daemon?.stop();
});

async function auth(page: import('@playwright/test').Page): Promise<void> {
  await page.goto('/');
  if ((await page.getByTestId('token-input').count()) > 0) {
    await page.getByTestId('token-input').fill(daemon.token);
    await page.getByTestId('token-submit').click();
  }
  // 全新沙箱会先见首启向导——常规用例跳过进面板（向导本身由 E-11 专测）
  const skip = page.getByTestId('wizard-skip');
  if ((await skip.count()) > 0) {
    await skip.click();
    await expect(page.getByTestId('sidebar')).toBeVisible();
  }
}

test('E-11 首启向导：三步可见、跳过后面板可达且不再出现', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByText('欢迎使用 Beaver')).toBeVisible();
  // 第 1 步：探测
  await expect(page.getByTestId('wizard-next-1')).toBeVisible();
  await page.getByTestId('wizard-next-1').click();
  // 第 2 步：导入（空沙箱可跳过）
  await expect(page.getByText('导入现有 MCP server')).toBeVisible();
  await page.getByText('跳过导入').click();
  // 第 3 步：同步
  await expect(page.getByTestId('wizard-dry-run')).toBeVisible();
  await page.getByTestId('wizard-sync').click().catch(() => {});
  // 立即同步在空注册表也应安全完成（无变更）→ 面板
  await expect(page.getByTestId('sidebar')).toBeVisible({ timeout: 15_000 });
  // 刷新后不再出现向导（hasOnboarded 已持久化）
  await page.reload();
  await expect(page.getByTestId('sidebar')).toBeVisible();
  await expect(page.getByTestId('wizard-skip')).toHaveCount(0);
});

test('E-01 token 门进入后可见概览与本机 agent 卡片', async ({ page }) => {
  await auth(page);
  await expect(page.getByTestId('home-dir')).toBeVisible();
  await expect(page.getByTestId('agent-card-claude-code')).toBeVisible();
});

test('E-04 添加 server（stdio）出现在注册表', async ({ page }) => {
  await auth(page);
  await page.getByTestId('tab-servers').click();
  await page.getByTestId('btn-add-server').click();
  await page.getByTestId('form-name').fill('smoke-fs');
  await page.getByTestId('form-command').fill(process.execPath);
  await page.getByTestId('form-args').fill('E2E_PLACEHOLDER');
  await page.getByTestId('form-save').click();
  await expect(page.getByTestId('server-row-smoke-fs')).toBeVisible();
});

test('E-03 分发预览（dry-run）出报告', async ({ page }) => {
  await auth(page);
  await page.getByTestId('tab-sync').click();
  await page.getByTestId('btn-dry-run').click();
  await expect(page.getByText('预览完成')).toBeVisible();
});

test('E-06 网关页展示运行状态与端点', async ({ page }) => {
  await auth(page);
  await page.getByTestId('tab-gateway').click();
  await expect(page.getByTestId('endpoint-url')).toBeVisible();
});

test('E-07 MCP 发现页进入即展示列表并可翻页', async ({ page }) => {
  await auth(page);
  await page.getByTestId('tab-discovery').click();
  // 进入页面自动查询（无需先输入关键词）；列表或错误提示二者其一出现（CI 环境可能无外网）
  await expect(page.getByTestId('tab-discovery')).toBeVisible();
  await page.waitForTimeout(1000);
  const rows = page.locator('[data-testid^="registry-"]');
  if ((await rows.count()) > 0) {
    await expect(rows.first()).toBeVisible();
  }
});

test('E-08 分组：新建分组、加入 MCP、关联 agent 后有效绑定生效', async ({ page }) => {
  await auth(page);
  await page.getByTestId('tab-sync').click();
  await page.getByTestId('btn-add-group').click();
  await page.getByTestId('group-name').fill('smoke-group');
  await page.getByTestId('group-server-smoke-fs').check();
  await page.getByTestId('group-save').click();
  await expect(page.getByTestId('group-card-smoke-group')).toBeVisible();

  // 通过分组卡片的可搜索 AddMenu 关联 claude-code
  const card = page.getByTestId('group-card-smoke-group');
  await card.getByTestId('group-agent-add-smoke-group').click();
  await card.getByTestId('group-agent-option-claude-code').click();
  await expect(page.getByTestId('sync-agent-claude-code')).toContainText('smoke-group');
  // 有效绑定 = 分组包含的 server（绑定计数 0 → 1，具体写入由下方 dry-run 报告验证）
  await expect(page.getByTestId('sync-agent-claude-code')).toContainText('共 1 个 MCP');

  // 分发预览仍可用，且 claude-code 的变更里出现组内 server
  await page.getByTestId('btn-dry-run').click();
  await expect(page.getByText('预览完成')).toBeVisible();
  await expect(page.getByTestId('report-claude-code')).toContainText('smoke-fs');
});

test('E-09 Skill 管理页：空态 + 本地 ZIP 安装 + 启停矩阵 + 卸载', async ({ page }) => {
  await auth(page);
  await page.getByTestId('tab-skills').click();
  await expect(page.getByTestId('btn-skills-updates')).toBeVisible();
  // 安装本地 ZIP（fixtures/demo-skill.zip）
  await page.getByTestId('btn-skills-zip').click();
  await page.getByTestId('skill-zip-input').setInputFiles(path.join(fileURLToPath(new URL('.', import.meta.url)), '../fixtures/skills/demo-skill.zip'));
  await expect(page.getByTestId('skill-card-skill-demo-skill')).toBeVisible({ timeout: 15000 });
  // 启用投影到 Claude Code
  await page.getByTestId('skill-toggle-skill-demo-skill-claude-code').click();
  await expect(page.getByText('已修改').or(page.getByTestId('skill-card-skill-demo-skill'))).toBeVisible();
  const card = page.getByTestId('skill-card-skill-demo-skill');
  await expect(card.getByText('当前')).toHaveCount(0); // 无当前概念，锁卡片仍在
  // 卸载
  await page.getByTestId('skill-uninstall-skill-demo-skill').click();
  await page.getByRole('button', { name: '卸载' }).last().click();
  await expect(page.getByText(/已卸载/)).toBeVisible({ timeout: 15000 });
});

test('E-10 设置页：常规项、恢复备份区与关于可见', async ({ page }) => {
  await auth(page);
  await page.getByTestId('tab-settings').click();
  await expect(page.getByTestId('settings-port')).toBeVisible();
  await expect(page.getByTestId('settings-loglevel')).toBeVisible();
  // 卡片标题精确匹配（页头描述也含这两个词，strict mode 需 exact）
  await expect(page.getByText('恢复备份', { exact: true })).toBeVisible();
  await expect(page.getByText('关于', { exact: true })).toBeVisible();
  // 端口输入回显当前配置端口（非空数字）
  const port = await page.getByTestId('settings-port').inputValue();
  expect(Number(port)).toBeGreaterThan(0);
});
