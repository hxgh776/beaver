import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport, getDefaultEnvironment } from '@modelcontextprotocol/sdk/client/stdio.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { SSEClientTransport } from '@modelcontextprotocol/sdk/client/sse.js';
import { decryptRecord } from '@beaver/core';
import type { Tool } from '@modelcontextprotocol/sdk/types.js';
import type { ServerDef } from '@beaver/core';
import type { Logger } from '../logger.js';

export type UpstreamStatus = 'idle' | 'starting' | 'ready' | 'error' | 'backoff';

const CONNECT_TIMEOUT_MS = 15_000;
const CALL_TIMEOUT_MS = 120_000;
const MAX_BACKOFF_MS = 30_000;
const STDERR_RING = 200;

/**
 * 单个上游 MCP server 的连接封装。
 * - 懒启动：首次使用才拉起进程/建连（G2）
 * - 崩溃自愈：指数退避重启（R6）
 * - stdio 串行化：上游多为单会话设计，请求排队执行（R3/G12 默认策略）
 */
export class Upstream {
  readonly def: ServerDef;
  status: UpstreamStatus = 'idle';
  lastError: string | null = null;
  pid?: number;
  readonly stderrRing: string[] = [];

  private client: Client | null = null;
  private transport: StdioClientTransport | StreamableHTTPClientTransport | SSEClientTransport | null = null;
  private connecting: Promise<Client> | null = null;
  private restartAttempts = 0;
  private restartTimer: NodeJS.Timeout | null = null;
  private used = false;
  // M3.2 G12：并发信号量（默认 1 = 串行，R3 最安全）
  private active = 0;
  private waiters: Array<() => void> = [];

  constructor(
    def: ServerDef,
    private readonly logger: Logger,
  ) {
    this.def = def;
  }

  get id(): string {
    return this.def.id;
  }

  /** 客户端可见的工具原始列表（未经聚合过滤） */
  toolsCache: Tool[] = [];

  async ensureClient(): Promise<Client> {
    if (this.client) return this.client;
    if (this.connecting) return this.connecting;
    this.used = true;
    this.status = 'starting';
    this.connecting = this.connect()
      .then((client) => {
        this.status = 'ready';
        this.lastError = null;
        this.restartAttempts = 0;
        return client;
      })
      .catch((err) => {
        this.status = 'error';
        this.lastError = String((err as Error).message ?? err);
        this.connecting = null;
        this.logger.error(`上游 ${this.id} 连接失败: ${this.lastError}`);
        throw err;
      });
    return this.connecting;
  }

  private async connect(): Promise<Client> {
    const client = new Client(
      { name: 'beaver-gateway', version: '0.1.0' },
      { capabilities: {} },
    );
    // S4：静态加密凭证在使用点解密（内存明文，不落盘）
    const env = await decryptRecord(this.def.env);
    const headers = await decryptRecord(this.def.headers);
    if (this.def.transport === 'stdio') {
      if (!this.def.command) throw new Error('stdio 上游缺少 command');
      this.transport = new StdioClientTransport({
        command: this.def.command,
        args: this.def.args ?? [],
        env: { ...getDefaultEnvironment(), ...(env ?? {}) },
        ...(this.def.cwd ? { cwd: this.def.cwd } : {}),
        stderr: 'pipe',
      });
    } else if (this.def.transport === 'sse') {
      // M2.3：legacy SSE 上游（D4 兼容）
      if (!this.def.url) throw new Error('sse 上游缺少 url');
      this.transport = new SSEClientTransport(new URL(this.def.url), {
        eventSourceInit: { fetch: (input, init) => fetch(input, { ...init, headers: { ...(headers ?? {}) } }) },
        requestInit: { headers: { ...(headers ?? {}) } },
      });
    } else {
      if (!this.def.url) throw new Error('http 上游缺少 url');
      this.transport = new StreamableHTTPClientTransport(new URL(this.def.url), {
        requestInit: {
          headers: {
            ...(headers ?? {}),
          },
        },
      });
    }

    client.onclose = () => {
      if (this.client === client) {
        this.client = null;
        this.connecting = null;
        this.scheduleRestart('连接关闭');
      }
    };
    client.onerror = (err) => {
      this.lastError = String(err);
    };

    // client.connect 内部会调用 transport.start()；连接成功后 stdio 可取子进程 pid
    await withTimeout(client.connect(this.transport), CONNECT_TIMEOUT_MS, '连接超时');
    if (this.transport instanceof StdioClientTransport) {
      this.pid = this.transport.pid ?? undefined;
      const stderr = this.transport.stderr;
      if (stderr) void this.readStderr(stderr as import('node:stream').Readable);
    }
    this.client = client;
    return client;
  }

  private async readStderr(stream: import('node:stream').Readable): Promise<void> {
    stream.setEncoding('utf8');
    let buffer = '';
    for await (const chunk of stream) {
      buffer += chunk;
      let idx: number;
      while ((idx = buffer.indexOf('\n')) >= 0) {
        const line = buffer.slice(0, idx).trimEnd();
        buffer = buffer.slice(idx + 1);
        if (line) {
          this.stderrRing.push(line);
          if (this.stderrRing.length > STDERR_RING) this.stderrRing.shift();
        }
      }
    }
  }

  /** 工具列表（带翻页追平与缓存） */
  async listTools(force = false): Promise<Tool[]> {
    if (!force && this.toolsCache.length > 0) return this.toolsCache;
    const client = await this.ensureClient();
    const tools: Tool[] = [];
    let cursor: string | undefined;
    do {
      const res = await withTimeout(client.listTools({ cursor }), CALL_TIMEOUT_MS, 'listTools 超时');
      tools.push(...res.tools);
      cursor = res.nextCursor;
    } while (cursor);
    this.toolsCache = tools;
    return tools;
  }

  /** 工具调用；stdio 上游按 concurrency 信号量限流（默认串行） */
  async callTool(toolName: string, args: Record<string, unknown> | undefined): Promise<unknown> {
    if (this.def.transport !== 'stdio') {
      const client = await this.ensureClient();
      return withTimeout(
        client.callTool({ name: toolName, arguments: args ?? {} }),
        CALL_TIMEOUT_MS,
        `工具 ${toolName} 调用超时`,
      );
    }
    await this.acquire();
    try {
      const client = await this.ensureClient();
      return await withTimeout(
        client.callTool({ name: toolName, arguments: args ?? {} }),
        CALL_TIMEOUT_MS,
        `工具 ${toolName} 调用超时`,
      );
    } finally {
      this.release();
    }
  }

  private async acquire(): Promise<void> {
    const limit = Math.max(1, this.def.concurrency ?? 1);
    if (this.active < limit) {
      this.active += 1;
      return;
    }
    await new Promise<void>((resolve) => this.waiters.push(resolve));
    this.active += 1;
  }

  private release(): void {
    this.active -= 1;
    this.waiters.shift()?.();
  }

  async health(): Promise<boolean> {
    try {
      const client = await this.ensureClient();
      await withTimeout(client.ping(), 5_000, 'ping 超时');
      return true;
    } catch {
      return false;
    }
  }

  /** 主动断开（stop/restart）；不触发自动重启 */
  async stop(): Promise<void> {
    this.used = false;
    if (this.restartTimer) {
      clearTimeout(this.restartTimer);
      this.restartTimer = null;
    }
    const client = this.client;
    this.client = null;
    this.connecting = null;
    this.toolsCache = [];
    this.pid = undefined;
    this.status = 'idle';
    if (client) {
      client.onclose = undefined;
      try {
        await client.close();
      } catch {
        // 忽略关闭错误
      }
    }
    if (this.transport) {
      try {
        await this.transport.close();
      } catch {
        // 子进程可能已死
      }
      this.transport = null;
    }
  }

  private scheduleRestart(reason: string): void {
    if (!this.used || this.restartTimer) return;
    const delay = Math.min(500 * 2 ** this.restartAttempts, MAX_BACKOFF_MS);
    this.restartAttempts += 1;
    this.status = 'backoff';
    this.logger.warn(`上游 ${this.id} 断开（${reason}），${delay}ms 后重连（第 ${this.restartAttempts} 次）`);
    this.restartTimer = setTimeout(() => {
      this.restartTimer = null;
      void this.ensureClient().catch(() => {});
    }, delay);
  }
}

export interface UpstreamSnapshot {
  id: string;
  status: UpstreamStatus;
  pid?: number;
  toolCount: number;
  lastError: string | null;
}

/** 上游集合管理：按注册表中 gatewayMode && enabled 的 server 建立连接 */
export class UpstreamManager {
  private upstreams = new Map<string, Upstream>();

  constructor(
    private readonly getDefs: () => ServerDef[],
    private readonly logger: Logger,
  ) {}

  /** 聚合端点包含的上游 */
  aggregateDefs(): ServerDef[] {
    return this.getDefs().filter((d) => d.gatewayMode && d.enabled);
  }

  has(id: string): boolean {
    return this.getDefs().some((d) => d.id === id && d.enabled);
  }

  get(id: string): Upstream | undefined {
    const def = this.getDefs().find((d) => d.id === id && d.enabled);
    if (!def) return undefined;
    let u = this.upstreams.get(id);
    if (!u || u.def !== def) {
      u = new Upstream(def, this.logger);
      this.upstreams.set(id, u);
    }
    return u;
  }

  peek(id: string): Upstream | undefined {
    return this.upstreams.get(id);
  }

  async stopAll(): Promise<void> {
    await Promise.all([...this.upstreams.values()].map((u) => u.stop()));
  }

  /**
   * 快照 = 已实例化的上游 + 启用中的网关型 server 的 idle 占位。
   * 懒启动下未被调用过的 server 也要出现在列表里，管理页才能区分「启用」（配置态）与「未运行/出错」（运行态）。
   */
  snapshots(): UpstreamSnapshot[] {
    const out = new Map<string, UpstreamSnapshot>();
    for (const d of this.getDefs()) {
      if (!d.enabled || !d.gatewayMode) continue;
      const u = this.upstreams.get(d.id);
      out.set(d.id, u !== undefined && u.def === d ? snapshotOf(u) : { id: d.id, status: 'idle', toolCount: 0, lastError: null });
    }
    for (const u of this.upstreams.values()) {
      if (!out.has(u.id)) out.set(u.id, snapshotOf(u));
    }
    return [...out.values()];
  }
}

function snapshotOf(u: Upstream): UpstreamSnapshot {
  return {
    id: u.id,
    status: u.status,
    ...(u.pid !== undefined ? { pid: u.pid } : {}),
    toolCount: u.toolsCache.length,
    lastError: u.lastError,
  };
}

function withTimeout<T>(promise: Promise<T>, ms: number, message: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(message)), ms);
    promise.then(
      (v) => {
        clearTimeout(timer);
        resolve(v);
      },
      (err) => {
        clearTimeout(timer);
        reject(err);
      },
    );
  });
}
