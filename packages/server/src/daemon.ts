import { existsSync, promises as fs } from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import type { IncomingMessage, ServerResponse } from 'node:http';
import {
  Store,
  SyncEngine,
  recoverPendingPublications,
  detectAgents,
  makeAgents,
} from '@beaver/core';
import { ProvidersService, SkillsService } from '@beaver/core';
import type { AgentDetection, ManagerConfig } from '@beaver/core';
import { Logger } from './logger.js';
import type { LogLevel } from './logger.js';
import { Router } from './router.js';
import { registerApiRoutes } from './api.js';
import { checkToken, sendError } from './http-util.js';
import { Gateway } from './gateway/gateway.js';

export const DAEMON_VERSION = '0.1.0';

export interface GatewayStatus {
  running: boolean;
  port?: number;
}

export type GatewayControlHook = (
  action: 'start' | 'stop',
) => Promise<{ running: boolean; port?: number }>;

export type GatewayHttpHandler = (
  req: IncomingMessage,
  res: ServerResponse,
  pathname: string,
  query: URLSearchParams,
) => Promise<boolean>;

export interface DaemonOptions {
  homeDir?: string;
  /**
   * agent 配置文件根目录（缺省为真实用户主目录）。
   * 测试/开发必须传沙箱目录，否则分发会写入真实 agent 配置！
   * 也可用环境变量 AGENT_CONTROL_AGENT_HOME（兼容旧名 MCP_MANAGER_AGENT_HOME）指定。
   */
  agentConfigRoot?: string;
  /** 测试可传 0 使用临时端口；不持久化 */
  port?: number;
  webDistDir?: string | null;
}

/**
 * Daemon：单进程承载 HTTP 服务（/api + /mcp 网关 + Web UI 静态托管）与配置内存态。
 * /mcp 相关钩子由 gateway 模块（M1.5）注入，核心保持协议无关。
 */
export class Daemon {
  readonly store: Store;
  readonly logger: Logger;
  readonly sync: SyncEngine;
  readonly skills: SkillsService;
  readonly providers: ProvidersService;
  readonly gateway: Gateway;
  config!: ManagerConfig;
  readonly router = new Router();

  gatewayControl?: GatewayControlHook;
  gatewayHttpHandler?: GatewayHttpHandler;
  gatewayStatus: () => GatewayStatus = () => ({ running: false });

  /** 以下钩子由 gateway 模块（M1.5）注入；缺省时 API 返回 503 */
  upstreamControl?: (id: string, action: 'start' | 'stop' | 'restart') => Promise<{ ok: boolean; state?: string }>;
  /** refresh=false 时优先返回缓存（上游已连过即秒回），true 强制重新抓取 */
  probeTools?: (id: string, refresh?: boolean) => Promise<Array<{ name: string; description?: string; enabled: boolean }> | null>;
  upstreamLogs?: (id: string) => string[];
  recentCalls?: () => unknown[];
  /** M3.1 调试台（S6） */
  invokeTool?: (id: string, tool: string, args: Record<string, unknown>) => Promise<unknown>;

  private httpServer?: http.Server;
  private startedAt = Date.now();
  private agentCache: AgentDetection[] | null = null;
  private readonly opts: DaemonOptions;
  private _actualPort = 0;

  constructor(opts: DaemonOptions = {}) {
    this.opts = opts;
    this.store = new Store(opts.homeDir);
    this.logger = new Logger(path.join(this.store.logsDir, 'daemon.log'));
    this.gateway = new Gateway(this);
    const agentRoot =
      opts.agentConfigRoot ?? process.env['AGENT_CONTROL_AGENT_HOME'] ?? process.env['MCP_MANAGER_AGENT_HOME'];
    this.sync = new SyncEngine({
      backupsDir: this.store.backupsDir,
      homeDir: this.store.homeDir,
      // G7：stdio 反向桥脚本与本 daemon 同目录（dist/bridge-main.js）
      stdioBridge: { command: 'node', baseArgs: [path.join(import.meta.dirname, 'bridge-main.js')] },
      ...(agentRoot ? { agents: makeAgents(agentRoot) } : {}),
    });
    this.skills = new SkillsService({
      store: this.store,
      getConfig: () => this.config,
      saveConfig: () => this.saveConfig(),
      ...(agentRoot ? { agentRoot } : {}),
    });
    this.providers = new ProvidersService({
      store: this.store,
      getConfig: () => this.config,
      saveConfig: () => this.saveConfig(),
      ...(agentRoot ? { agentRoot } : {}),
    });
    if (!agentRoot && (opts.homeDir || process.env['AGENT_CONTROL_HOME'] || process.env['MCP_MANAGER_HOME'])) {
      this.logger.warn(
        '数据目录已重定向但 agent 配置仍指向真实用户主目录——测试场景请传 agentConfigRoot',
      );
    }
  }

  get actualPort(): number {
    return this._actualPort;
  }

  get baseUrl(): string {
    return `http://127.0.0.1:${this._actualPort}`;
  }

  async start(): Promise<void> {
    this.config = await this.store.load();
    this.logger.setLevel(this.config.settings.logLevel as LogLevel);
    registerApiRoutes(this.router, this);

    const desired = this.opts.port ?? this.config.settings.port;
    const server = http.createServer((req, res) => {
      void this.handle(req, res);
    });
    this.httpServer = server;

    let bound = false;
    let lastError: unknown;
    for (let i = 0; i <= 10; i++) {
      const port = desired + i;
      try {
        await listen(server, port);
        this._actualPort = actualPortOf(server);
        bound = true;
        break;
      } catch (err) {
        lastError = err;
        if ((err as NodeJS.ErrnoException).code !== 'EADDRINUSE') break;
      }
    }
    if (!bound) throw lastError ?? new Error('无法绑定端口');

    // 端口漂移时持久化（测试用的临时端口除外）
    if (this.opts.port === undefined && this._actualPort !== this.config.settings.port) {
      this.config.settings.port = this._actualPort;
      await this.saveConfig();
    }

    // S0：崩溃前滚——判定上次未清账的发布意图（只上报，不改文件内容）
    const recovered = await recoverPendingPublications(this.store.homeDir);
    if (recovered) {
      for (const r of recovered) {
        this.logger.warn(`上次发布未正常收账: ${r.path} → ${r.state}`);
      }
    }

    // 网关钩子常驻挂载；运行时开关见 /api/gateway/start|stop
    this.gateway.attach();

    this.logger.info(`daemon started at ${this.baseUrl} (home: ${this.store.homeDir})`);
  }

  async stop(): Promise<void> {
    const server = this.httpServer;
    if (!server) return;
    await this.gateway.detach().catch(() => {});
    server.closeAllConnections(); // 先断残余 keep-alive，否则 close 回调迟迟不触发
    await new Promise<void>((resolve) => server.close(() => resolve()));
    this.httpServer = undefined;
    this.logger.info('daemon stopped');
  }

  async saveConfig(): Promise<void> {
    await this.store.save(this.config);
  }

  async agentDetections(force = false): Promise<AgentDetection[]> {
    if (!this.agentCache || force) {
      this.agentCache = await detectAgents();
    }
    return this.agentCache;
  }

  uptimeSec(): number {
    return Math.floor((Date.now() - this.startedAt) / 1000);
  }

  private async handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1');
    const pathname = url.pathname;
    try {
      // 1) MCP 网关端点（含 per-server 与 per-agent 分组端点）
      if (
        pathname === '/mcp' ||
        pathname.startsWith('/servers/') ||
        pathname.startsWith('/agents/')
      ) {
        if (this.gatewayHttpHandler) {
          const handled = await this.gatewayHttpHandler(req, res, pathname, url.searchParams);
          if (handled) return;
        }
        sendError(res, 503, 'gateway 未启动', 'GATEWAY_DOWN');
        return;
      }

      // 2) 管理 API（D7：authRequired 开启时校验 Bearer token；/api/auth/info 永远公开）
      if (pathname === '/api' || pathname.startsWith('/api/')) {
        const isPublic = pathname === '/api/auth/info';
        if (!isPublic && this.config.settings.authRequired === true &&
            !checkToken(req, this.config.settings.token, url.searchParams)) {
          sendError(res, 401, '未授权', 'UNAUTHORIZED');
          return;
        }
        const handled = await this.router.dispatch(req, res, pathname, url.searchParams);
        if (!handled) sendError(res, 404, 'not found');
        return;
      }

      // 3) Web UI 静态资源
      await this.serveStatic(pathname, res);
    } catch (err) {
      this.logger.error('request failed', { pathname, err: String(err) });
      if (!res.headersSent) sendError(res, 500, String((err as Error).message ?? err));
      else res.end();
    }
  }

  private async serveStatic(pathname: string, res: ServerResponse): Promise<void> {
    const dist = this.opts.webDistDir === undefined ? resolveDefaultWebDist() : this.opts.webDistDir;
    if (!dist) {
      sendError(res, 404, 'Web UI 未安装（web/dist 缺失），可用 CLI 或 /api 操作');
      return;
    }
    let file = path.normalize(path.join(dist, pathname === '/' ? 'index.html' : pathname));
    if (!file.startsWith(path.normalize(dist))) {
      sendError(res, 403, 'forbidden');
      return;
    }
    try {
      await fs.access(file);
    } catch {
      // SPA 路由回退
      if (!path.extname(pathname)) {
        file = path.join(dist, 'index.html');
      } else {
        sendError(res, 404, 'not found');
        return;
      }
    }
    const ext = path.extname(file).toLowerCase();
    const type =
      ext === '.html' ? 'text/html; charset=utf-8'
      : ext === '.js' ? 'text/javascript; charset=utf-8'
      : ext === '.css' ? 'text/css; charset=utf-8'
      : ext === '.svg' ? 'image/svg+xml'
      : ext === '.json' ? 'application/json'
      : ext === '.png' ? 'image/png'
      : ext === '.ico' ? 'image/x-icon'
      : ext === '.woff2' ? 'font/woff2'
      : 'application/octet-stream';
    const data = await fs.readFile(file);
    res.writeHead(200, { 'content-type': type, 'content-length': data.length });
    res.end(data);
  }
}

function listen(server: http.Server, port: number): Promise<void> {
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', () => {
      server.removeListener('error', reject);
      resolve();
    });
  });
}

function actualPortOf(server: http.Server): number {
  const addr = server.address();
  return typeof addr === 'object' && addr !== null ? addr.port : 0;
}

function resolveDefaultWebDist(): string | null {
  // npm 安装态：bundle 同目录的 web/（esbuild 打包时内嵌）；workspace 态：<repo>/packages/web/dist
  for (const candidate of [
    path.resolve(import.meta.dirname, 'web'),
    path.resolve(import.meta.dirname, '..', '..', 'web', 'dist'),
  ]) {
    if (existsSync(candidate)) return candidate;
  }
  return null;
}
