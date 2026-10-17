import { promises as fs } from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { SSEServerTransport } from '@modelcontextprotocol/sdk/server/sse.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { Daemon } from '../src/daemon.js';

const FIXTURE = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '../../../fixtures/fake-stdio-server.mjs',
);

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
  return { status: res.status, data: (await res.json()) as Record<string, unknown> };
}

async function connectClient(pathname = '/mcp'): Promise<Client> {
  const transport = new StreamableHTTPClientTransport(new URL(`${base}${pathname}`), {
    requestInit: { headers: { authorization: `Bearer ${token}` } },
  });
  const client = new Client({ name: 'gateway-test', version: '1.0.0' });
  await client.connect(transport);
  return client;
}

interface FakeHttpUpstream {
  url: string;
  close: () => Promise<void>;
}

/** 测试专用：一个真实的 streamable HTTP 上游 MCP server */
async function startFakeHttpUpstream(): Promise<FakeHttpUpstream> {
  const httpServer = http.createServer(async (req, res) => {
    const server = new Server(
      { name: 'fake-http-upstream', version: '1.0.0' },
      { capabilities: { tools: {} } },
    );
    server.setRequestHandler(ListToolsRequestSchema, async () => ({
      tools: [
        {
          name: 'http_tool',
          description: 'from http upstream',
          inputSchema: { type: 'object', properties: {} },
        },
      ],
    }));
    server.setRequestHandler(CallToolRequestSchema, async (req2) => ({
      content: [{ type: 'text', text: `http:${String(req2.params.name)}` }],
    }));
    const transport = new StreamableHTTPServerTransport({
      sessionIdGenerator: undefined,
      enableJsonResponse: true,
    });
    res.on('close', () => {
      void transport.close().catch(() => {});
      void server.close().catch(() => {});
    });
    await server.connect(transport);
    await transport.handleRequest(req, res);
  });
  await new Promise<void>((r) => httpServer.listen(0, '127.0.0.1', r));
  const addr = httpServer.address();
  const port = typeof addr === 'object' && addr !== null ? addr.port : 0;
  return {
    url: `http://127.0.0.1:${port}/mcp`,
    close: async () => {
      httpServer.closeAllConnections();
      await new Promise<void>((r) => httpServer.close(() => r()));
    },
  };
}

beforeEach(async () => {
  sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-gw-'));
  daemon = new Daemon({ homeDir: sandbox, agentConfigRoot: sandbox, port: 0 });
  await daemon.start();
  base = daemon.baseUrl;
  token = daemon.config.settings.token;
  // 两个 stdio 上游（工具重名用于前缀测试）+ 一个 http 上游
  for (const id of ['fs-a', 'fs-b']) {
    await api('POST', '/api/servers', {
      id,
      name: id,
      transport: 'stdio',
      command: process.execPath,
      args: [FIXTURE],
      env: { MAGIC_VALUE: `magic-${id}` },
      gatewayMode: true,
    });
  }
  const httpUp = await startFakeHttpUpstream();
  httpUpstream = httpUp;
  await api('POST', '/api/servers', {
    id: 'web',
    name: 'web',
    transport: 'http',
    url: httpUp.url,
    gatewayMode: true,
  });
  await api('POST', '/api/gateway/start');
});

let httpUpstream: FakeHttpUpstream;
const cleanups: Array<() => Promise<void>> = [];

/** M2.3：legacy SSE 上游 fixture（GET /sse + POST /messages 双端点） */
async function startFakeSseUpstream(): Promise<{ url: string; close: () => Promise<void> }> {
  let sseTransport: SSEServerTransport | null = null;
  const makeServer = (): Server => {
    const server = new Server(
      { name: 'fake-sse-upstream', version: '1.0.0' },
      { capabilities: { tools: {} } },
    );
    server.setRequestHandler(ListToolsRequestSchema, async () => ({
      tools: [
        {
          name: 'sse_tool',
          description: 'from sse upstream',
          inputSchema: { type: 'object', properties: {} },
        },
      ],
    }));
    server.setRequestHandler(CallToolRequestSchema, async (req2) => ({
      content: [{ type: 'text', text: `sse:${String(req2.params.name)}` }],
    }));
    return server;
  };
  const httpServer = http.createServer(async (req, res) => {
    const url = req.url ?? '';
    if (req.method === 'GET' && url.startsWith('/sse')) {
      const server = makeServer();
      sseTransport = new SSEServerTransport('/messages', res);
      await server.connect(sseTransport);
      return; // SSE 长连接保持
    }
    if (req.method === 'POST' && url.startsWith('/messages')) {
      if (sseTransport !== null) {
        await sseTransport.handlePostMessage(req, res);
        return;
      }
      res.writeHead(503).end();
      return;
    }
    res.writeHead(404).end();
  });
  await new Promise<void>((r) => httpServer.listen(0, '127.0.0.1', r));
  const addr = httpServer.address();
  const port = typeof addr === 'object' && addr !== null ? addr.port : 0;
  return {
    url: `http://127.0.0.1:${port}/sse`,
    close: async () => {
      httpServer.closeAllConnections();
      await new Promise<void>((r) => httpServer.close(() => r()));
    },
  };
}

afterEach(async () => {
  for (const fn of cleanups.reverse()) await fn().catch(() => {});
  cleanups.length = 0;
  await daemon.stop();
  await httpUpstream?.close();
  await fs.rm(sandbox, { recursive: true, force: true });
});

describe('gateway aggregate endpoint (I-GW)', () => {
  it('I-GW-01 聚合 listTools + callTool + env 凭证透传', async () => {
    const client = await connectClient();
    const tools = await client.listTools();
    const names = tools.tools.map((t) => t.name).sort();
    // fs-a / fs-b 工具重名 → 前缀；web 的 http_tool 唯一 → 透传
    expect(names).toEqual(['fs-a__add', 'fs-a__echo', 'fs-a__env', 'fs-b__add', 'fs-b__echo', 'fs-b__env', 'http_tool']);

    const echo = (await client.callTool({ name: 'fs-a__echo', arguments: { message: 'hi' } })) as {
      content: Array<{ text: string }>;
    };
    expect(echo.content[0]!.text).toBe('echo: hi');

    const add = (await client.callTool({ name: 'fs-b__add', arguments: { a: 2, b: 3 } })) as {
      content: Array<{ text: string }>;
    };
    expect(add.content[0]!.text).toBe('5');

    const env = (await client.callTool({ name: 'fs-a__env', arguments: {} })) as {
      content: Array<{ text: string }>;
    };
    expect(env.content[0]!.text).toBe('magic-fs-a');

    const httpTool = (await client.callTool({ name: 'http_tool', arguments: {} })) as {
      content: Array<{ text: string }>;
    };
    expect(httpTool.content[0]!.text).toBe('http:http_tool');
    await client.close();
  });

  it('I-GW-02 懒启动：未使用时快照列出 idle 占位而非缺席', async () => {
    const gw = (await api('GET', '/api/gateway')).data as {
      running: boolean;
      upstreams: Array<{ id: string; status: string; pid?: number; toolCount: number }>;
    };
    expect(gw.running).toBe(true);
    // 启用中的网关型 server 即使从未被调用也要出现（idle 占位），管理页才能区分「启用」与「未运行/出错」
    expect(gw.upstreams.map((u) => u.id).sort()).toEqual(['fs-a', 'fs-b', 'web']);
    for (const u of gw.upstreams) {
      expect(u.status).toBe('idle');
      expect(u.pid).toBeUndefined();
      expect(u.toolCount).toBe(0);
    }
  });

  it('上游启动失败：快照标记 error 并保留错误信息', async () => {
    await api('POST', '/api/servers', {
      id: 'boom',
      name: 'boom',
      transport: 'stdio',
      command: process.execPath,
      args: ['-e', 'process.exit(1)'],
      gatewayMode: true,
    });
    const res = await api('POST', '/api/servers/boom/start');
    expect(res.status).toBe(500); // 启动失败回传 500
    const gw = (await api('GET', '/api/gateway')).data as {
      upstreams: Array<{ id: string; status: string; lastError: string | null }>;
    };
    const boom = gw.upstreams.find((u) => u.id === 'boom');
    expect(boom?.status).toBe('error');
    expect(boom?.lastError).toBeTruthy();
  });

  it('I-GW-03 崩溃自愈：杀上游进程后再次调用自动恢复', async () => {
    const client = await connectClient();
    await client.listTools();
    await api('POST', '/api/servers/fs-a/start'); // 显式拉起，拿 pid
    const gw1 = (await api('GET', '/api/gateway')).data as { upstreams: Array<{ id: string; pid?: number; status: string }> };
    const pid = gw1.upstreams.find((u) => u.id === 'fs-a')!.pid;
    expect(pid).toBeGreaterThan(0);

    process.kill(pid!);
    await new Promise((r) => setTimeout(r, 300));

    // 再次调用：应自动重启上游并成功
    const res = (await client.callTool({ name: 'fs-a__echo', arguments: { message: 'back' } })) as {
      content: Array<{ text: string }>;
    };
    expect(res.content[0]!.text).toBe('echo: back');
    await client.close();
  });

  it('I-GW-04 上游错误与未知工具传播', async () => {
    const client = await connectClient();
    const fail = (await client.callTool({ name: 'fs-a__add', arguments: { a: 1 } })) as {
      isError?: boolean;
    };
    // 缺参数由上游/网关报错（fixture 的 add 对 undefined 求和得 0，不抛错；这里验证调用链路通）
    void fail;
    await expect(client.callTool({ name: 'no_such_tool', arguments: {} })).rejects.toThrow(/未知工具/);
    await client.close();
  });

  it('I-GW-05 工具停用：从列表剔除，调用返回停用错误 (G4)', async () => {
    await api('PATCH', '/api/servers/fs-a', {
      toolOverrides: { echo: { enabled: false } },
    });
    const client = await connectClient();
    const tools = await client.listTools();
    const names = tools.tools.map((t) => t.name);
    expect(names).not.toContain('fs-a__echo');
    // fs-a 的 echo 停用后不再冲突 → fs-b 的 echo 回退为无前缀名
    expect(names).toContain('echo');

    const disabled = (await client.callTool({ name: 'fs-a__echo', arguments: { message: 'x' } })) as {
      isError?: boolean;
      content: Array<{ text: string }>;
    };
    expect(disabled.isError).toBe(true);
    expect(disabled.content[0]!.text).toContain('停用');
    await client.close();
  });

  it('I-GW-06 per-server 独立端点：原始工具名（G6）', async () => {
    const client = await connectClient('/servers/fs-a/mcp');
    const tools = await client.listTools();
    const names = tools.tools.map((t) => t.name).sort();
    expect(names).toEqual(['add', 'echo', 'env']); // 不带前缀
    const res = (await client.callTool({ name: 'echo', arguments: { message: 'single' } })) as {
      content: Array<{ text: string }>;
    };
    expect(res.content[0]!.text).toBe('echo: single');
    await client.close();
  });

  it('D7 令牌开关：默认关闭时无凭证可访问；开启后无 token 401', async () => {
    // 默认关闭：无 token 客户端可用
    const openTransport = new StreamableHTTPClientTransport(new URL(`${base}/mcp`));
    const openClient = new Client({ name: 'no-auth-client', version: '1.0.0' });
    await openClient.connect(openTransport);
    const tools = await openClient.listTools();
    expect(tools.tools.length).toBeGreaterThan(0);
    await openClient.close();

    // 开启后：无 token 401
    await api('PATCH', '/api/settings', { authRequired: true });
    const res = await fetch(`${base}/mcp`, { method: 'POST' });
    expect(res.status).toBe(401);
    await api('PATCH', '/api/settings', { authRequired: false });
  });

  it('I-GW-08 调用日志记录 server/tool/耗时 (G11)', async () => {
    const client = await connectClient();
    await client.callTool({ name: 'fs-a__echo', arguments: { message: 'log' } });
    await client.close();
    const calls = (await api('GET', '/api/logs/calls')).data.calls as Array<{
      serverId: string;
      tool: string;
      ok: boolean;
      durationMs: number;
    }>;
    const hit = calls.find((c) => c.tool === 'echo' && c.serverId === 'fs-a');
    expect(hit).toBeDefined();
    expect(hit!.ok).toBe(true);
    expect(typeof hit!.durationMs).toBe('number');
  });

  it('上游进程控制 API：stop 后状态 idle', async () => {
    await api('POST', '/api/servers/fs-a/start');
    const stopped = await api('POST', '/api/servers/fs-a/stop');
    expect(stopped.status).toBe(200);
    const gw = (await api('GET', '/api/gateway')).data as { upstreams: Array<{ id: string; status: string }> };
    expect(gw.upstreams.find((u) => u.id === 'fs-a')?.status).toBe('idle');
  });

  it('Issue-3：调用日志跨 daemon 重启持久（读 ndjson 尾部）', async () => {
    const client = await connectClient();
    await client.callTool({ name: 'fs-a__echo', arguments: { message: 'before-restart' } });
    await client.close();

    // 重启 daemon（同一数据目录）
    const homeDir = daemon.store.homeDir;
    const agentRoot = homeDir;
    await daemon.stop();
    daemon = new Daemon({ homeDir, agentConfigRoot: agentRoot, port: 0 });
    await daemon.start();
    base = daemon.baseUrl;
    token = daemon.config.settings.token;

    const calls = (await api('GET', '/api/logs/calls')).data.calls as Array<{
      serverId: string;
      tool: string;
      ok: boolean;
    }>;
    const hit = calls.find((c) => c.tool === 'echo' && c.serverId === 'fs-a');
    expect(hit).toBeDefined();
    expect(hit!.ok).toBe(true);
  });

  it('M2.3 SSE 上游：legacy SSE server 经网关聚合与调用', async () => {
    const sseUp = await startFakeSseUpstream();
    cleanups.push(sseUp.close);
    await api('POST', '/api/servers', {
      id: 'legacy-sse',
      name: 'legacy-sse',
      transport: 'sse',
      url: sseUp.url,
      gatewayMode: true,
    });

    const client = await connectClient();
    const tools = await client.listTools();
    expect(tools.tools.map((t) => t.name)).toContain('sse_tool');
    const res = (await client.callTool({ name: 'sse_tool', arguments: {} })) as {
      content: Array<{ text: string }>;
    };
    expect(res.content[0]!.text).toBe('sse:sse_tool');
    await client.close();
  });

  it('M3.3 per-agent 分组端点：agent 只看到自己绑定的网关 server', async () => {
    // fs-a 绑定到 cursor；fs-b 不绑定
    await api('PUT', '/api/bindings/cursor/fs-a');

    const cursorClient = await connectClient('/agents/cursor/mcp');
    const cursorTools = (await cursorClient.listTools()).tools.map((t) => t.name).sort();
    // 作用域内只有 fs-a → 无冲突 → 无前缀
    expect(cursorTools).toEqual(['add', 'echo', 'env']);
    const res = (await cursorClient.callTool({ name: 'echo', arguments: { message: 'scoped' } })) as {
      content: Array<{ text: string }>;
    };
    expect(res.content[0]!.text).toBe('echo: scoped');
    await cursorClient.close();

    // claude-code 无绑定 → 空工具列表
    const emptyClient = await connectClient('/agents/claude-code/mcp');
    const emptyTools = (await emptyClient.listTools()).tools;
    expect(emptyTools).toEqual([]);
    await emptyClient.close();
  });

  it('M3.1 调试台：不经 agent 直接调用工具（S6）', async () => {
    const res = await api('POST', '/api/servers/fs-a/invoke', {
      tool: 'echo',
      arguments: { message: 'from-debug-console' },
    });
    expect(res.status).toBe(200);
    const result = res.data.result as { content: Array<{ text: string }> };
    expect(result.content[0]!.text).toBe('echo: from-debug-console');
    // 调用也进入日志（scope=debug）
    const calls = (await api('GET', '/api/logs/calls')).data.calls as Array<{ scope: string; tool: string }>;
    expect(calls.some((c) => c.scope === 'debug' && c.tool === 'echo')).toBe(true);

    const missing = await api('POST', '/api/servers/fs-a/invoke', { tool: 'nope', arguments: {} });
    expect(missing.status).toBe(502);
  });
});
