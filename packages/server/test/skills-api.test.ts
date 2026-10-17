import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Daemon } from '../src/daemon.js';

let sandbox: string;
let daemon: Daemon;
let base: string;
let token: string;

const fixtureDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '../../../fixtures/skills');

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
  sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-skillapi-'));
  daemon = new Daemon({ homeDir: sandbox, agentConfigRoot: sandbox, port: 0 });
  await daemon.start();
  base = daemon.baseUrl;
  token = daemon.config.settings.token;
});

afterEach(async () => {
  await daemon.stop();
  await fs.rm(sandbox, { recursive: true, force: true });
  vi.unstubAllGlobals();
});

describe('skills API (A-SK)', () => {
  it('空列表 → 安装 ZIP → 启停投影 → 卸载', async () => {
    const empty = await api('GET', '/api/skills');
    expect(empty.status).toBe(200);
    expect((empty.data.skills as unknown[]).length).toBe(0);

    const zip = await fs.readFile(path.join(fixtureDir, 'demo-skill.zip'));
    const installed = await api('POST', '/api/skills/install-zip', {
      contentBase64: zip.toString('base64'),
    });
    expect(installed.status).toBe(200);
    const skill = installed.data.skill as Record<string, unknown>;
    expect(skill['directory']).toBe('demo-skill');
    expect(await fs.readdir(path.join(sandbox, 'skills', 'demo-skill'))).toContain('SKILL.md');

    // 启用 → 投影到沙箱 .claude/skills
    const id = String(skill['id']);
    const on = await api('POST', `/api/skills/${id}/toggle`, { agentType: 'claude-code', enabled: true });
    expect(on.status).toBe(200);
    const projected = path.join(sandbox, '.claude', 'skills', 'demo-skill', 'SKILL.md');
    expect((await fs.readFile(projected, 'utf8')).includes('Demo')).toBe(true);

    // 列表状态 projected
    const listed = await api('GET', '/api/skills');
    const row = (listed.data.skills as Array<Record<string, unknown>>)[0]!;
    const cc = (row['targets'] as Array<Record<string, unknown>>).find((t) => t.agentType === 'claude-code')!;
    expect(cc['projected']).toBe(true);

    // 停用 → 投影移除
    await api('POST', `/api/skills/${id}/toggle`, { agentType: 'claude-code', enabled: false });
    await expect(fs.access(projected)).rejects.toThrow();

    // 卸载 → 备份 + 清注册
    const removed = await api('DELETE', `/api/skills/${id}`);
    expect(removed.status).toBe(200);
    expect(removed.data.preserved).toEqual([]);
    const after = await api('GET', '/api/skills');
    expect((after.data.skills as unknown[]).length).toBe(0);
    const backups = await fs.readdir(path.join(sandbox, 'skill-backups'));
    expect(backups.length).toBe(1);
  });

  it('非法仓库 → 400；ZIP 破损 → 400', async () => {
    const bad = await api('POST', '/api/skills/install', { repo: 'skills.volces.com/x' });
    expect(bad.status).toBe(400);

    const broken = await api('POST', '/api/skills/install-zip', {
      contentBase64: Buffer.from('this is not a zip').toString('base64'),
    });
    expect(broken.status).toBe(400);
  });

  it('搜索：透传 skills.sh（stub fetch）；上游挂 → 502', async () => {
    // stub 必须只拦 skills.sh：测试自身打向 daemon 的 fetch 也走 globalThis
    const real = globalThis.fetch;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: Parameters<typeof fetch>[0], init?: RequestInit) => {
        const url = String(input);
        if (url.includes('skills.sh/api/search')) {
          return new Response(
            JSON.stringify({
              count: 1,
              skills: [{ skillId: 'find-skills', name: 'find-skills', installs: 42, source: 'anthropics/skills' }],
            }),
            { status: 200 },
          );
        }
        return real(input, init);
      }) as unknown as typeof fetch,
    );
    const okRes = await api('GET', '/api/skills/search?q=find');
    expect(okRes.status).toBe(200);
    expect((okRes.data.skills as Array<Record<string, unknown>>)[0]!['repo']).toBe('anthropics/skills');

    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: Parameters<typeof fetch>[0], init?: RequestInit) => {
        const url = String(input);
        if (url.includes('skills.sh/api/search')) return new Response('boom', { status: 500 });
        return real(input, init);
      }) as unknown as typeof fetch,
    );
    const down = await api('GET', '/api/skills/search?q=find');
    expect(down.status).toBe(502);
  });

  it('集合安装：非法 repo → 400；stub 归档 → 一次收编全部技能并投影', async () => {
    const bad = await api('POST', '/api/skills/install-collection', { repo: 'skills.volces.com/x' });
    expect(bad.status).toBe(400);

    const zip = await fs.readFile(path.join(fixtureDir, 'multi-skill.zip'));
    const real = globalThis.fetch;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: Parameters<typeof fetch>[0], init?: RequestInit) => {
        const url = String(input);
        if (url.startsWith('https://github.com/test/repo/archive/')) {
          return new Response(new Uint8Array(zip), { status: 200 });
        }
        return real(input, init);
      }) as unknown as typeof fetch,
    );
    const res = await api('POST', '/api/skills/install-collection', {
      repo: 'test/repo',
      enableFor: ['claude-code'],
    });
    expect(res.status).toBe(200);
    const report = res.data as { installed: Array<Record<string, unknown>>; failed: unknown[] };
    expect(report.installed.map((s) => s.directory).sort()).toEqual(['alpha', 'beta']);
    expect(report.failed).toEqual([]);
    // enableFor 生效：两个技能已投影到 claude-code 的 skills 目录
    expect(await fs.readdir(path.join(sandbox, '.claude', 'skills'))).toEqual(
      expect.arrayContaining(['alpha', 'beta']),
    );
  });
});
