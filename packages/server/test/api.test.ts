import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Daemon } from '../src/daemon.js';
import { Store } from '@beaver/core';

let sandbox: string;
let daemon: Daemon;
let base: string;
let token: string;

async function api(
  method: string,
  pathname: string,
  body?: unknown,
  useToken?: string,
): Promise<{ status: number; data: Record<string, unknown> }> {
  const res = await fetch(`${base}${pathname}`, {
    method,
    headers: {
      authorization: `Bearer ${useToken ?? token}`,
      ...(body !== undefined ? { 'content-type': 'application/json' } : {}),
    },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  const data = (await res.json()) as Record<string, unknown>;
  return { status: res.status, data };
}

beforeEach(async () => {
  sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-apitest-'));
  // 沙箱纪律：agent 配置也必须重定向，绝不触碰真实 agent 配置
  daemon = new Daemon({ homeDir: sandbox, agentConfigRoot: sandbox, port: 0 });
  await daemon.start();
  base = daemon.baseUrl;
  token = daemon.config.settings.token;
});

afterEach(async () => {
  vi.unstubAllGlobals();
  await daemon.stop();
  await fs.rm(sandbox, { recursive: true, force: true });
});

describe('auth (D7 令牌开关)', () => {
  it('默认关闭：无 token 直接 200', async () => {
    const noAuth = await fetch(`${base}/api/status`);
    expect(noAuth.status).toBe(200);
    const info = (await (await fetch(`${base}/api/auth/info`)).json()) as { authRequired: boolean };
    expect(info.authRequired).toBe(false);
  });

  it('开启后：无/错 token 401，query token 放行；关闭恢复', async () => {
    await api('PATCH', '/api/settings', { authRequired: true });
    const noAuth = await fetch(`${base}/api/status`);
    expect(noAuth.status).toBe(401);
    const wrong = await api('GET', '/api/status', undefined, 'wrong-token');
    expect(wrong.status).toBe(401);
    const viaQuery = await fetch(`${base}/api/status?token=${token}`);
    expect(viaQuery.status).toBe(200);
    // auth/info 仍然公开
    const info = await fetch(`${base}/api/auth/info`);
    expect(info.status).toBe(200);
    // 关闭恢复
    await api('PATCH', '/api/settings', { authRequired: false });
    expect((await fetch(`${base}/api/status`)).status).toBe(200);
  });
});

describe('registry CRUD (I-AP-01)', () => {
  it('创建→列表→PATCH→删除 全链路', async () => {
    const created = await api('POST', '/api/servers', {
      name: 'Filesystem',
      transport: 'stdio',
      command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-filesystem'],
      env: { FS_ROOT: 'D:\\x' },
    });
    expect(created.status).toBe(201);
    const id = (created.data.server as { id: string }).id;
    expect(id).toBe('filesystem');

    const listed = await api('GET', '/api/servers');
    expect((listed.data.servers as unknown[]).length).toBe(1);

    const patched = await api('PATCH', `/api/servers/${id}`, { gatewayMode: true });
    expect((patched.data.server as { gatewayMode: boolean }).gatewayMode).toBe(true);

    const toolOverride = await api('PATCH', `/api/servers/${id}`, {
      toolOverrides: { write_file: { enabled: false } },
    });
    expect(
      (toolOverride.data.server as { toolOverrides: Record<string, { enabled: boolean }> }).toolOverrides['write_file']
        ?.enabled,
    ).toBe(false);

    const dup = await api('POST', '/api/servers', { name: 'Filesystem', transport: 'stdio', command: 'npx' });
    expect((dup.data.server as { id: string }).id).toBe('filesystem-2');

    const removed = await api('DELETE', `/api/servers/${id}`);
    expect(removed.status).toBe(200);
    expect(((await api('GET', '/api/servers')).data.servers as unknown[]).length).toBe(1);

    const invalid = await api('POST', '/api/servers', { name: 'X', transport: 'stdio' });
    expect(invalid.status).toBe(400); // 缺 command
    // Issue-2：校验错误为友好中文文案，而非 zod 原始 JSON
    const msg = String(invalid.data.error);
    expect(msg).toContain('命令');
    expect(msg).toContain('stdio');
    expect(msg).not.toContain('too_small');
  });
});

describe('sync via API (I-SY over HTTP)', () => {
  it('dryRun 不落盘；真实同步写入沙箱配置', async () => {
    await api('POST', '/api/servers', {
      name: 'fs',
      transport: 'stdio',
      command: 'node',
      args: ['server.js'],
    });
    await api('PUT', '/api/bindings/cursor/fs');

    const dry = await api('POST', '/api/sync', { dryRun: true });
    expect(dry.status).toBe(200);
    const cursorFile = path.join(sandbox, '.cursor', 'mcp.json');
    await expect(fs.access(cursorFile)).rejects.toThrow();

    const real = await api('POST', '/api/sync', {});
    expect(real.status).toBe(200);
    const doc = JSON.parse(await fs.readFile(cursorFile, 'utf8'));
    expect(doc.mcpServers['fs'].command).toBe('node');
  });
});

describe('agents + settings (I-AP-02)', () => {
  it('agents 探测结构与 settings 修改/token 轮换', async () => {
    const agents = await api('GET', '/api/agents');
    const list = agents.data.agents as Array<{ agentType: string; transports: string[] }>;
    expect(list.map((a) => a.agentType)).toContain('claude-code');

    const patched = await api('PATCH', '/api/settings', { logLevel: 'debug' });
    expect(patched.status).toBe(200);
    expect(((await api('GET', '/api/settings')).data as { logLevel: string }).logLevel).toBe('debug');

    const badPort = await api('PATCH', '/api/settings', { port: 99999 });
    expect(badPort.status).toBe(400);

    // 轮换在令牌开启时才有"待重同步"语义
    await api('PATCH', '/api/settings', { authRequired: true });
    const rotate = await api('POST', '/api/settings/token/rotate');
    const newToken = (rotate.data as { token: string }).token;
    expect(newToken).not.toBe(token);
    // 旧 token 立即失效
    const oldAuth = await api('GET', '/api/status', undefined, token);
    expect(oldAuth.status).toBe(401);
    token = newToken;
  });
});

describe('gateway runtime switch', () => {
  it('/mcp 令牌开启时无 token → 401；网关停止后带 token 也 503', async () => {
    await api('PATCH', '/api/settings', { authRequired: true });
    const noToken = await fetch(`${base}/mcp`, { method: 'POST' });
    expect(noToken.status).toBe(401);
    const stopped = await api('POST', '/api/gateway/stop');
    expect(stopped.status).toBe(200);
    const down = await fetch(`${base}/mcp`, {
      method: 'POST',
      headers: { authorization: `Bearer ${token}` },
    });
    expect(down.status).toBe(503);
    expect(((await down.json()) as { code?: string }).code).toBe('GATEWAY_DOWN');
    await api('PATCH', '/api/settings', { authRequired: false });
  });
});

describe('M2 additions', () => {
  it('M2.2 环境探测：node 必在且 found', async () => {
    const env = await api('GET', '/api/environment');
    expect(env.status).toBe(200);
    const tools = env.data.tools as Array<{ name: string; found: boolean; role: string }>;
    const node = tools.find((t) => t.name === 'node')!;
    expect(node.found).toBe(true);
    expect(tools.map((t) => t.name)).toContain('uvx');
  });

  it('M2.4 token 轮换提醒：rotate 后 resyncPending，网关重写后清除', async () => {
    // 令牌开启时轮换才有重同步语义（D7：关闭时网关条目不含 token）
    await api('PATCH', '/api/settings', { authRequired: true });
    // 注册一个网关模式 server 并绑定（产生网关条目）
    await api('POST', '/api/servers', {
      name: 'gw-srv', transport: 'stdio', command: 'node', args: ['x.js'], gatewayMode: true,
    });
    await api('PUT', '/api/bindings/cursor/gw-srv');
    await api('POST', '/api/sync', {});
    expect(((await api('GET', '/api/status')).data as { resyncPending: boolean }).resyncPending).toBe(false);

    const rotated = await api('POST', '/api/settings/token/rotate');
    token = (rotated.data as { token: string }).token; // 后续请求必须用新 token（旧 token 已 401）
    expect(((await api('GET', '/api/status')).data as { resyncPending: boolean }).resyncPending).toBe(true);

    // 重新同步：网关条目带新 token 重写 → 提醒消除
    await api('POST', '/api/sync', {});
    expect(((await api('GET', '/api/status')).data as { resyncPending: boolean }).resyncPending).toBe(false);
    await api('PATCH', '/api/settings', { authRequired: false });
  });
});

describe('store round-trip through daemon', () => {
  it('配置变更被持久化，可被下一个 Store 实例读到', async () => {
    await api('POST', '/api/servers', { name: 'persist', transport: 'stdio', command: 'node' });
    const reloaded = await new Store(sandbox).load();
    expect(reloaded.servers.map((s) => s.id)).toContain('persist');
  });
});

// stub 只拦上游 registry：测试自身打向 daemon 的 fetch 走 globalThis 透传
function stubUpstream(map: Array<{ match: (url: string) => boolean; body: unknown; status?: number }>): void {
  const real = globalThis.fetch;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: Parameters<typeof fetch>[0], init?: RequestInit) => {
      const url = String(input);
      const hit = map.find((m) => m.match(url));
      if (hit) return new Response(JSON.stringify(hit.body), { status: hit.status ?? 200 });
      return real(input, init);
    }) as unknown as typeof fetch,
  );
}

describe('registry 搜索：官方条目 MCPB 缺安装配置 → npm 同名回退；npm 源相关性过滤', () => {
  const officialMcpbEntry = {
    servers: [
      {
        server: {
          name: 'io.github.DeusData/codebase-memory-mcp',
          title: 'Codebase Memory',
          description: 'Persistent codebase knowledge graph.',
          version: '0.1.3',
          packages: [
            { registryType: 'mcpb', identifier: 'https://github.com/x/y/releases/download/v0.1.3/y-win.zip', transport: { type: 'stdio' } },
          ],
        },
      },
    ],
    metadata: { total: 1 },
  };

  it('官方条目仅 mcpb 包 → 按名回查 npm 补 npx 建议，描述附回退说明', async () => {
    stubUpstream([
      { match: (u) => u.startsWith('https://registry.modelcontextprotocol.io/v0/servers'), body: officialMcpbEntry },
      { match: (u) => u.endsWith('/codebase-memory-mcp/latest'), body: { name: 'codebase-memory-mcp', bin: { 'codebase-memory-mcp': 'bin.js' } } },
    ]);
    const res = await api('GET', '/api/registry/search?q=codebase-memory-mcp&limit=20&offset=0&source=official');
    expect(res.status).toBe(200);
    const servers = res.data.servers as Array<Record<string, unknown>>;
    expect(servers).toHaveLength(1);
    expect(servers[0]!['suggestion']).toMatchObject({
      name: 'codebase-memory-mcp',
      transport: 'stdio',
      command: 'npx',
      args: ['-y', 'codebase-memory-mcp'],
      gatewayMode: true,
    });
    expect(String(servers[0]!['description'])).toContain('npm 包回退');
  });

  it('npm 无同名包（404）→ suggestion 保持 null，不出假建议', async () => {
    stubUpstream([
      { match: (u) => u.startsWith('https://registry.modelcontextprotocol.io/v0/servers'), body: officialMcpbEntry },
      { match: (u) => u.endsWith('/codebase-memory-mcp/latest'), body: { error: 'Not Found' }, status: 404 },
    ]);
    const res = await api('GET', '/api/registry/search?q=codebase-memory-mcp&limit=20&offset=0&source=official');
    expect(res.status).toBe(200);
    const servers = res.data.servers as Array<Record<string, unknown>>;
    expect(servers[0]!['suggestion']).toBeNull();
  });

  it('npm 源：相关性过滤生效，同名精确包排第一（不被高下载模糊匹配埋掉）', async () => {
    stubUpstream([
      {
        match: (u) => u.includes('registry.npmjs.org/-/v1/search'),
        body: {
          total: 3,
          objects: [
            { package: { name: '@better-auth/memory-adapter', description: 'Memory adapter' }, downloads: { weekly: 11054730 } },
            { package: { name: 'codebase-memory-mcp', description: 'Fast code intelligence', version: '0.12.1' }, downloads: { weekly: 8008 } },
            { package: { name: 'mongodb-memory-server', description: 'MongoDB in-memory' }, downloads: { weekly: 2164713 } },
          ],
        },
      },
    ]);
    const res = await api('GET', '/api/registry/search?q=codebase-memory-mcp&limit=20&offset=0&source=npm');
    expect(res.status).toBe(200);
    const servers = res.data.servers as Array<{ name: string }>;
    expect(servers.map((s) => s.name)).toEqual(['codebase-memory-mcp']);
  });
});
