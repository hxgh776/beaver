import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
  PingRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';
import type { Tool } from '@modelcontextprotocol/sdk/types.js';
import type { ServerDef } from '@beaver/core';
import { UpstreamManager } from './upstream.js';
import type { Upstream } from './upstream.js';

export interface CallRecord {
  ts: string;
  scope: string;
  serverId: string;
  tool: string;
  ok: boolean;
  durationMs: number;
  error?: string;
}

export interface ToolIndexEntry {
  upstream: Upstream;
  originalName: string;
}

export function isToolEnabled(def: ServerDef, toolName: string): boolean {
  return def.toolOverrides?.[toolName]?.enabled !== false;
}

/**
 * 聚合 MCP server（G1/G3/G4/G5/G13）。
 *
 * - tools/list：合并所有就绪上游（可按 filter 过滤，用于 per-agent 分组端点）；
 *   冲突工具名加 `serverId__` 前缀（映射表供调用路由）；
 *   被停用的工具直接从列表剔除（上下文成本控制）；
 * - tools/call：经映射路由回上游；未知工具回退 `serverId__tool` 前缀解析；
 * - 单上游失败不影响其余（Promise.allSettled），错误记日志。
 */
export function createAggregateServer(mgr: UpstreamManager, opts: {
  scope: string;
  onCall: (record: CallRecord) => void;
  toolIndex: Map<string, ToolIndexEntry>;
  filter?: (def: { id: string }) => boolean;
}): Server {
  const server = new Server(
    { name: 'beaver', version: '0.1.0' },
    { capabilities: { tools: {} } },
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => {
    const upstreams = mgr
      .aggregateDefs()
      .filter((d) => opts.filter === undefined || opts.filter(d))
      .map((d) => mgr.get(d.id))
      .filter((u): u is Upstream => u !== undefined);

    const results = await Promise.allSettled(upstreams.map((u) => u.listTools()));
    const nameCounts = new Map<string, number>();
    const collected: Array<{ upstream: Upstream; tools: Tool[] }> = [];
    results.forEach((r, i) => {
      const upstream = upstreams[i]!;
      if (r.status === 'rejected') return; // 单上游失败不影响整体
      const enabled = r.value.filter((t) => isToolEnabled(upstream.def, t.name));
      for (const t of enabled) nameCounts.set(t.name, (nameCounts.get(t.name) ?? 0) + 1);
      collected.push({ upstream, tools: enabled });
    });

    const tools: Tool[] = [];
    for (const { upstream, tools: upstreamTools } of collected) {
      for (const t of upstreamTools) {
        const exposed = (nameCounts.get(t.name) ?? 0) > 1 ? `${upstream.id}__${t.name}` : t.name;
        opts.toolIndex.set(exposed, { upstream, originalName: t.name });
        tools.push({ ...t, name: exposed });
      }
    }
    return { tools };
  });

  server.setRequestHandler(CallToolRequestSchema, async (request) => {
    const { name, arguments: args } = request.params;
    const started = Date.now();
    let entry = opts.toolIndex.get(name);
    if (!entry) {
      // 前缀回退：serverId 不含下划线（slug），按第一个 __ 拆分
      const idx = name.indexOf('__');
      if (idx > 0) {
        const upstream = mgr.get(name.slice(0, idx));
        if (upstream) entry = { upstream, originalName: name.slice(idx + 2) };
      }
    }
    const record = (ok: boolean, error?: string): void => {
      opts.onCall({
        ts: new Date().toISOString(),
        scope: opts.scope,
        serverId: entry?.upstream.id ?? 'unknown',
        tool: entry?.originalName ?? name,
        ok,
        durationMs: Date.now() - started,
        ...(error !== undefined ? { error } : {}),
      });
    };
    if (!entry) {
      record(false, `未知工具: ${name}`);
      throw new Error(`未知工具: ${name}`);
    }
    if (!isToolEnabled(entry.upstream.def, entry.originalName)) {
      record(false, `工具已停用: ${name}`);
      return {
        content: [{ type: 'text', text: `工具 ${name} 已被停用（beaver）` }],
        isError: true,
      };
    }
    try {
      const result = (await entry.upstream.callTool(entry.originalName, args)) as {
        isError?: boolean;
      };
      record(!result.isError, result.isError ? '上游返回错误' : undefined);
      return result;
    } catch (err) {
      record(false, String((err as Error).message ?? err));
      throw err;
    }
  });

  server.setRequestHandler(PingRequestSchema, async () => ({}));
  return server;
}

/** per-server 独立端点（G6）：薄反代到单个上游，工具名保持原始形态 */
export function createSingleServerServer(mgr: UpstreamManager, upstreamId: string, opts: {
  scope: string;
  onCall: (record: CallRecord) => void;
}): Server {
  const server = new Server(
    { name: `beaver:${upstreamId}`, version: '0.1.0' },
    { capabilities: { tools: {} } },
  );
  const upstream = () => mgr.get(upstreamId);

  server.setRequestHandler(ListToolsRequestSchema, async () => {
    const u = upstream();
    if (!u) throw new Error(`上游不存在: ${upstreamId}`);
    const tools = await u.listTools();
    return { tools: tools.filter((t) => isToolEnabled(u.def, t.name)) };
  });

  server.setRequestHandler(CallToolRequestSchema, async (request) => {
    const { name, arguments: args } = request.params;
    const u = upstream();
    if (!u) throw new Error(`上游不存在: ${upstreamId}`);
    const started = Date.now();
    if (!isToolEnabled(u.def, name)) {
      return {
        content: [{ type: 'text', text: `工具 ${name} 已被停用（beaver）` }],
        isError: true,
      };
    }
    try {
      const result = (await u.callTool(name, args)) as { isError?: boolean };
      opts.onCall({
        ts: new Date().toISOString(),
        scope: opts.scope,
        serverId: upstreamId,
        tool: name,
        ok: !result.isError,
        durationMs: Date.now() - started,
        ...(result.isError ? { error: '上游返回错误' } : {}),
      });
      return result;
    } catch (err) {
      opts.onCall({
        ts: new Date().toISOString(),
        scope: opts.scope,
        serverId: upstreamId,
        tool: name,
        ok: false,
        durationMs: Date.now() - started,
        error: String((err as Error).message ?? err),
      });
      throw err;
    }
  });

  server.setRequestHandler(PingRequestSchema, async () => ({}));
  return server;
}
