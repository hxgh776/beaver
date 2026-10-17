import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { Daemon } from '../src/daemon.js';

let sandbox: string;
let daemon: Daemon;
let base: string;
let token: string;

async function api(
  method: string,
  pathname: string,
  body?: unknown,
): Promise<{ status: number; data: Record<string, unknown> }> {
  const res = await fetch(`${base}${pathname}`, {
    method,
    headers: {
      authorization: `Bearer ${token}`,
      ...(body !== undefined ? { 'content-type': 'application/json' } : {}),
    },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  const data = (await res.json()) as Record<string, unknown>;
  return { status: res.status, data };
}

beforeEach(async () => {
  sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-provapi-'));
  daemon = new Daemon({ homeDir: sandbox, agentConfigRoot: sandbox, port: 0 });
  await daemon.start();
  base = daemon.baseUrl;
  token = daemon.config.settings.token;
});

afterEach(async () => {
  await daemon.stop();
  await fs.rm(sandbox, { recursive: true, force: true });
});

describe('providers API (A-PR)', () => {
  it('预设 → 创建档案 → 切换 → 当前指示 → 删除', async () => {
    const empty = await api('GET', '/api/providers');
    expect(empty.status).toBe(200);
    expect(empty.data.apps).toHaveLength(3);

    // 预设目录（数量与形状）
    const presets = await api('GET', '/api/providers/presets?app=claude-code');
    expect(presets.status).toBe(200);
    expect((presets.data.presets as unknown[]).length).toBeGreaterThanOrEqual(90);
    const kimi = (presets.data.presets as Array<Record<string, unknown>>).find((p) => p.name === 'Kimi');
    expect(kimi).toBeDefined();

    // 从预设创建（带 Key）
    const created = await api('POST', '/api/providers/from-preset', {
      app: 'claude-code',
      presetName: 'Kimi',
      apiKey: 'sk-kimi-test',
    });
    expect(created.status).toBe(200);
    const profile = created.data.profile as Record<string, unknown>;
    expect(profile['name']).toBe('Kimi');

    // 切换 → settings.json 投影
    const switched = await api('POST', `/api/providers/${String(profile['id'])}/switch`, {});
    expect(switched.status).toBe(200);
    const settings = JSON.parse(
      await fs.readFile(path.join(sandbox, '.claude', 'settings.json'), 'utf8'),
    ) as Record<string, unknown>;
    const env = settings['env'] as Record<string, unknown>;
    expect(env['ANTHROPIC_AUTH_TOKEN']).toBe('sk-kimi-test');
    expect(env['ANTHROPIC_BASE_URL']).toBe('https://api.moonshot.cn/anthropic');

    // 当前指示
    const after = await api('GET', '/api/providers');
    const claude = (after.data.apps as Array<Record<string, unknown>>).find((a) => a.app === 'claude-code')!;
    expect(claude['current']).toBe(profile['id']);

    // 删除
    const removed = await api('DELETE', `/api/providers/${String(profile['id'])}`);
    expect(removed.status).toBe(200);
    const final = await api('GET', '/api/providers');
    const claudeFinal = (final.data.apps as Array<Record<string, unknown>>).find((a) => a.app === 'claude-code')!;
    expect(claudeFinal['current']).toBeNull();
  });

  it('非法输入 → 400；预设不存在 → 404', async () => {
    const bad = await api('POST', '/api/providers/from-preset', { app: 'claude-code', presetName: 'Nope' });
    expect(bad.status).toBe(404);
    const badApp = await api('GET', '/api/providers/presets?app=nope');
    expect(badApp.status).toBe(400);
    const badProfile = await api('POST', '/api/providers', { app: 'claude-code', name: '' });
    expect(badProfile.status).toBe(400);
  });
});
