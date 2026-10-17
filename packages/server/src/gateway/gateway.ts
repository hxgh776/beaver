import { promises as fs, statSync, readFileSync, openSync, readSync, closeSync } from 'node:fs';
import path from 'node:path';
import { effectiveBoundIds } from '@beaver/core';
import type { AgentType } from '@beaver/core';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { checkToken, sendError } from '../http-util.js';
import type { Daemon, GatewayHttpHandler, GatewayStatus } from '../daemon.js';
import { UpstreamManager } from './upstream.js';
import type { UpstreamSnapshot } from './upstream.js';
import { createAggregateServer, createSingleServerServer } from './aggregate.js';
import type { CallRecord, ToolIndexEntry } from './aggregate.js';

const CALLS_LOG_MAX_MEMORY = 500;

/**
 * 网关（模块 2 核心）：借宿 daemon 的 HTTP 端口，激活 /mcp 与 /servers/:id/mcp。
 * start/stop 只是挂载/卸载 HTTP 钩子并断开上游，网关本身不占独立端口。
 */
export class Gateway {
  private mgr: UpstreamManager;
  private running = false;
  /** M3.3：每个 endpoint scope 一个工具索引（stateless 下跨请求持存） */
  private toolIndexes = new Map<string, Map<string, ToolIndexEntry>>();
  private recentCalls: CallRecord[] = [];
  private callsLogFile: string;

  constructor(private readonly daemon: Daemon) {
    this.mgr = new UpstreamManager(
      () => daemon.config?.servers ?? [],
      daemon.logger,
    );
    this.callsLogFile = path.join(daemon.store.logsDir, 'gateway-calls.ndjson');
  }

  private toolIndexFor(scope: string): Map<string, ToolIndexEntry> {
    let idx = this.toolIndexes.get(scope);
    if (idx === undefined) {
      idx = new Map();
      this.toolIndexes.set(scope, idx);
    }
    return idx;
  }

  /** M3.1 调试台（S6）：直接调用某 server 的某工具 */
  async invokeTool(id: string, tool: string, args: Record<string, unknown>): Promise<unknown> {
    const upstream = this.mgr.get(id);
    if (!upstream) throw new Error(`上游不存在或未启用: ${id}`);
    const started = Date.now();
    try {
      const result = await upstream.callTool(tool, args);
      this.recordCall({
        ts: new Date().toISOString(),
        scope: 'debug',
        serverId: id,
        tool,
        ok: !(result as { isError?: boolean }).isError,
        durationMs: Date.now() - started,
      });
      return result;
    } catch (err) {
      this.recordCall({
        ts: new Date().toISOString(),
        scope: 'debug',
        serverId: id,
        tool,
        ok: false,
        durationMs: Date.now() - started,
        error: String((err as Error).message ?? err),
      });
      throw err;
    }
  }

  get status(): GatewayStatus {
    return this.running
      ? { running: true, port: this.daemon.actualPort }
      : { running: false };
  }

  upstreamSnapshots(): UpstreamSnapshot[] {
    return this.mgr.snapshots();
  }

  recentCallsList(): CallRecord[] {
    return this.recentCalls;
  }

  /** 挂载到 daemon（幂等） */
  attach(): void {
    if (this.running) return;
    this.running = true;
    this.daemon.gatewayHttpHandler = this.httpHandler();
    this.daemon.gatewayControl = async (action) => {
      if (action === 'start') {
        this.attach();
      } else {
        this.detach();
      }
      return this.status;
    };
    this.daemon.gatewayStatus = () => this.status;
    this.daemon.upstreamControl = async (id, action) => {
      const upstream = this.mgr.get(id);
      if (!upstream) throw new Error(`上游不存在或未启用: ${id}`);
      if (action === 'stop') await upstream.stop();
      else if (action === 'start') await upstream.ensureClient();
      else {
        await upstream.stop();
        await upstream.ensureClient();
      }
      const snap = this.mgr.snapshots().find((s) => s.id === id);
      return { ok: true, state: snap?.status };
    };
    this.daemon.probeTools = async (id, refresh = false) => {
      const upstream = this.mgr.get(id);
      if (!upstream) return null;
      try {
        // 缓存优先：工具列表抓取慢（需拉起上游进程），已抓过直接返回；refresh=1 才强制重抓
        const tools = await upstream.listTools(!refresh);
        return tools.map((t) => ({
          name: t.name,
          ...(t.description ? { description: t.description } : {}),
          enabled: t.name ? this.enabledFor(id, t.name) : true,
        }));
      } catch {
        return null;
      }
    };
    this.daemon.upstreamLogs = (id) => this.mgr.peek(id)?.stderrRing ?? [];
    // Issue-3：从 ndjson 尾部读取，daemon 重启后日志不丢；文件不可读时回退内存环
    this.daemon.recentCalls = () => this.readRecentCallsFromDisk();
    // M3.1 调试台（S6）
    this.daemon.invokeTool = (id, tool, args) => this.invokeTool(id, tool, args);
    this.daemon.logger.info('gateway attached');
  }

  async detach(): Promise<void> {
    this.running = false;
    this.daemon.gatewayHttpHandler = undefined;
    await this.mgr.stopAll();
    this.daemon.logger.info('gateway detached');
  }

  private enabledFor(serverId: string, toolName: string): boolean {
    const def = this.daemon.config.servers.find((s) => s.id === serverId);
    if (!def) return false;
    return def.toolOverrides?.[toolName]?.enabled !== false;
  }

  private recordCall(record: CallRecord): void {
    this.recentCalls.push(record);
    if (this.recentCalls.length > CALLS_LOG_MAX_MEMORY) this.recentCalls.shift();
    void fs.appendFile(this.callsLogFile, `${JSON.stringify(record)}\n`, 'utf8').catch(() => {});
  }

  /** 读取 ndjson 尾部（最多 256KB / 200 条），跳过损坏行 */
  private readRecentCallsFromDisk(): CallRecord[] {
    try {
      const st = statSync(this.callsLogFile);
      if (!st.isFile() || st.size === 0) return this.recentCalls;
      const TAIL = 256 * 1024;
      const start = Math.max(0, st.size - TAIL);
      let text: string;
      if (start === 0) {
        text = readFileSync(this.callsLogFile, 'utf8');
      } else {
        const fd = openSync(this.callsLogFile, 'r');
        try {
          const buf = Buffer.alloc(st.size - start);
          readSync(fd, buf, 0, buf.length, start);
          text = buf.toString('utf8');
        } finally {
          closeSync(fd);
        }
      }
      const lines = text.split('\n').filter((l) => l.trim() !== '');
      if (start > 0 && lines.length > 0) lines.shift(); // 首行可能被截断
      const out: CallRecord[] = [];
      for (const line of lines) {
        try {
          out.push(JSON.parse(line) as CallRecord);
        } catch {
          // 跳过损坏行
        }
      }
      return out.slice(-200);
    } catch {
      return this.recentCalls;
    }
  }

  private httpHandler(): GatewayHttpHandler {
    return async (req, res, pathname, query) => {
      if (!this.running) {
        sendError(res, 503, 'gateway 未启动', 'GATEWAY_DOWN');
        return true;
      }
      // D7：仅当访问令牌开启时校验；关闭时依赖环回监听兜底
      if (this.daemon.config.settings.authRequired === true &&
          !checkToken(req, this.daemon.config.settings.token, query)) {
        sendError(res, 401, '未授权', 'UNAUTHORIZED');
        return true;
      }

      let upstreamId: string | null = null;
      let agentScope: string | null = null;
      if (pathname !== '/mcp') {
        const perServer = /^\/servers\/([^/]+)\/mcp$/.exec(pathname);
        const perAgent = /^\/agents\/([^/]+)\/mcp$/.exec(pathname);
        if (perServer !== null) {
          upstreamId = decodeURIComponent(perServer[1]!);
          if (!this.mgr.has(upstreamId)) {
            sendError(res, 404, `上游不存在或未启用: ${upstreamId}`);
            return true;
          }
        } else if (perAgent !== null) {
          agentScope = decodeURIComponent(perAgent[1]!);
        } else {
          sendError(res, 404, 'not found');
          return true;
        }
      }

      const transport = new StreamableHTTPServerTransport({
        sessionIdGenerator: undefined, // stateless：规避各 agent 会话行为差异（§1.5-C）
        enableJsonResponse: true,
      });
      let server;
      let scope: string;
      if (upstreamId !== null) {
        scope = `single:${upstreamId}`;
        server = createSingleServerServer(this.mgr, upstreamId, {
          scope,
          onCall: (r) => this.recordCall(r),
        });
      } else if (agentScope !== null) {
        // per-agent 分组端点——该 agent 的有效绑定（直连 + 分组）网关 server 子集
        scope = `agent:${agentScope}`;
        const bound = effectiveBoundIds(this.daemon.config, agentScope as AgentType);
        server = createAggregateServer(this.mgr, {
          scope,
          onCall: (r) => this.recordCall(r),
          toolIndex: this.toolIndexFor(scope),
          filter: (d) => bound.has(d.id),
        });
      } else {
        scope = 'aggregate';
        server = createAggregateServer(this.mgr, {
          scope,
          onCall: (r) => this.recordCall(r),
          toolIndex: this.toolIndexFor(scope),
        });
      }
      res.on('close', () => {
        void transport.close().catch(() => {});
        void server.close().catch(() => {});
      });
      await server.connect(transport);
      await transport.handleRequest(req, res);
      return true;
    };
  }
}
