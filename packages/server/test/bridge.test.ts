/* M2.1 G7 桥集成测试：真实 bridge-main.js 子进程 ↔ 真实 streamable HTTP 上游 ↔ stdio SDK client。 */
import { promises as fs } from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';
import { afterEach, describe, expect, it } from 'vitest';

const BRIDGE_MAIN = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '../dist/bridge-main.js',
);

interface FakeHttpUpstream {
  url: string;
  close: () => Promise<void>;
}

let authSeen: string | null = null;

async function startFakeHttpUpstream(requireAuth = false): Promise<FakeHttpUpstream> {
  const httpServer = http.createServer(async (req, res) => {
    if (requireAuth) {
      authSeen = req.headers['authorization'] ?? null;
      if (authSeen !== 'Bearer bridge-test-token') {
        res.writeHead(401, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ error: 'unauthorized' }));
        return;
      }
    }
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
      content: [{ type: 'text', text: `http:${String(req2.params.name)}:${JSON.stringify(req2.params.arguments ?? {})}` }],
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

const cleanups: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const fn of cleanups.reverse()) await fn().catch(() => {});
  cleanups.length = 0;
});

describe('G7 stdio 反向桥（M2.1）', () => {
  it('桥进程把 HTTP 上游暴露为 stdio：listTools + callTool 透传', async () => {
    const upstream = await startFakeHttpUpstream(false);
    cleanups.push(upstream.close);

    const sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-bridge-'));
    const transport = new StdioClientTransport({
      command: process.execPath,
      args: [BRIDGE_MAIN, upstream.url, '--name', 'bridged'],
      stderr: 'pipe',
      cwd: sandbox,
    });
    const client = new Client({ name: 'bridge-test', version: '1.0.0' });
    await client.connect(transport);
    cleanups.push(async () => {
      await client.close().catch(() => {});
    });

    const tools = await client.listTools();
    expect(tools.tools.map((t) => t.name)).toEqual(['http_tool']);
    const res = (await client.callTool({ name: 'http_tool', arguments: { k: 1 } })) as {
      content: Array<{ text: string }>;
    };
    expect(res.content[0]!.text).toBe('http:http_tool:{"k":1}');
  });

  it('--token 透传为 Authorization: Bearer', async () => {
    const upstream = await startFakeHttpUpstream(true);
    cleanups.push(upstream.close);

    const transport = new StdioClientTransport({
      command: process.execPath,
      args: [BRIDGE_MAIN, upstream.url, '--token=bridge-test-token'],
      stderr: 'pipe',
    });
    const client = new Client({ name: 'bridge-test', version: '1.0.0' });
    await client.connect(transport);
    cleanups.push(async () => {
      await client.close().catch(() => {});
    });

    const tools = await client.listTools();
    expect(tools.tools.map((t) => t.name)).toEqual(['http_tool']);
    expect(authSeen).toBe('Bearer bridge-test-token');
  });
});
