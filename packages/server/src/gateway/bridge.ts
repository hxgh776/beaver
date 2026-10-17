import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
  PingRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';
import type { Tool } from '@modelcontextprotocol/sdk/types.js';

/**
 * G7 stdio 反向桥：把一个 streamable HTTP 上游暴露为本地 stdio MCP server。
 * 让只支持 stdio 的 agent（如 Codex）用上远程 HTTP server 与网关聚合端点。
 *
 * 独立于 daemon 运行（纯透传、无本地状态）；由同步引擎写为
 * `node <server dist>/bridge-main.js <url> --token=... [--header k=v ...]`。
 */

export interface BridgeOptions {
  url: string;
  headers?: Record<string, string>;
  name?: string;
}

export function parseBridgeArgs(argv: string[]): BridgeOptions {
  let url: string | null = null;
  const headers: Record<string, string> = {};
  let name: string | undefined;
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]!;
    if (arg === '--header' && argv[i + 1]) {
      const kv = argv[i + 1]!;
      const idx = kv.indexOf('=');
      if (idx > 0) headers[kv.slice(0, idx)] = kv.slice(idx + 1);
      i++;
    } else if (arg.startsWith('--header=')) {
      const kv = arg.slice('--header='.length);
      const idx = kv.indexOf('=');
      if (idx > 0) headers[kv.slice(0, idx)] = kv.slice(idx + 1);
    } else if (arg === '--token' && argv[i + 1]) {
      headers['Authorization'] = `Bearer ${argv[i + 1]}`;
      i++;
    } else if (arg.startsWith('--token=')) {
      headers['Authorization'] = `Bearer ${arg.slice('--token='.length)}`;
    } else if (arg === '--name' && argv[i + 1]) {
      name = argv[i + 1];
      i++;
    } else if (arg.startsWith('--name=')) {
      name = arg.slice('--name='.length);
    } else if (!arg.startsWith('--') && url === null) {
      url = arg;
    }
  }
  if (url === null) throw new Error('用法: bridge <url> [--token=xxx] [--header k=v ...] [--name xxx]');
  return { url, ...(Object.keys(headers).length > 0 ? { headers } : {}), ...(name ? { name } : {}) };
}

export function createBridgeServer(opts: BridgeOptions): Server {
  let client: Client | null = null;
  let connecting: Promise<Client> | null = null;

  const upstream = (): Promise<Client> => {
    if (client) return Promise.resolve(client);
    if (!connecting) {
      connecting = (async () => {
        const c = new Client({ name: 'beaver-bridge', version: '0.1.0' }, { capabilities: {} });
        const transport = new StreamableHTTPClientTransport(new URL(opts.url), {
          requestInit: { headers: { ...(opts.headers ?? {}) } },
        });
        await c.connect(transport);
        c.onclose = () => {
          client = null;
          connecting = null;
        };
        client = c;
        return c;
      })().catch((err) => {
        connecting = null;
        throw err;
      });
    }
    return connecting;
  };

  const server = new Server(
    { name: opts.name ?? 'beaver-bridge', version: '0.1.0' },
    { capabilities: { tools: {} } },
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => {
    const c = await upstream();
    // 翻页在此处追平（stdio agent 按无 cursor 终止即可）
    const tools: Tool[] = [];
    let cursor: string | undefined;
    do {
      const res = await c.listTools({ cursor });
      tools.push(...res.tools);
      cursor = res.nextCursor;
    } while (cursor);
    return { tools };
  });

  server.setRequestHandler(CallToolRequestSchema, async (request) => {
    const c = await upstream();
    const { name, arguments: args } = request.params;
    return await c.callTool({ name, arguments: args ?? {} });
  });

  server.setRequestHandler(PingRequestSchema, async () => {
    const c = await upstream();
    await c.ping();
    return {};
  });

  return server;
}

/** 独立入口：连接 stdio 并服务直到 stdin 关闭 */
export async function runBridgeStdio(opts: BridgeOptions): Promise<void> {
  const server = createBridgeServer(opts);
  const transport = new StdioServerTransport();
  await server.connect(transport);
}
