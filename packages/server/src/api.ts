import { randomBytes } from 'node:crypto';
import {
  applyImport,
  baiduMcpWorldToSuggestions,
  clineCatalogToSuggestions,
  decryptRecord,
  dedupeSuggestions,
  deleteGroup,
  deleteServer,
  detectEnvironment,
  dockerCatalogToSuggestions,
  effectiveBoundIds,
  encryptRecord,
  filterByRelevance,
  groupAddServer,
  groupBindAgent,
  groupRemoveServer,
  groupUnbindAgent,
  importPreview,
  mcpmRegistryToSuggestions,
  modelscopeToSuggestions,
  baiduMcpWorldTotal,
  npmManifestToSuggestion,
  npmSearchToSuggestions,
  registryEntryToServerInput,
  removeBinding,
  setBinding,
  smitheryToSuggestions,
  sortByPopularity,
  upsertGroup,
  upsertServer,
  SKILL_AGENT_LIST,
  presetEnv,
} from '@beaver/core';
import type { ProviderApp } from '@beaver/core';
import type { AgentType, ServerDef, SkillAgentType, UpsertServerInput } from '@beaver/core';import { z } from 'zod';
import type { Daemon } from './daemon.js';
import { sendError, sendJson } from './http-util.js';
import type { Router } from './router.js';

const serverInputObject = z.object({
  id: z.string().regex(/^[a-z0-9][a-z0-9-]*$/).optional(),
  name: z.string().min(1),
  transport: z.enum(['stdio', 'http', 'sse']),
  command: z.string().optional(),
  args: z.array(z.string()).optional(),
  env: z.record(z.string()).optional(),
  cwd: z.string().optional(),
  url: z.string().optional(),
  headers: z.record(z.string()).optional(),
  gatewayMode: z.boolean().optional(),
  enabled: z.boolean().optional(),
  toolOverrides: z.record(z.object({ enabled: z.boolean() })).optional(),
  concurrency: z.number().int().min(1).max(16).optional(),
});

/** 创建/整体更新：传输类型与必需字段匹配 */
const serverInputSchema = serverInputObject.superRefine((val, ctx) => {
  if (val.transport === 'stdio' && !val.command) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'stdio 传输需要 command', path: ['command'] });
  }
  if (val.transport === 'http' && !val.url) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'http 传输需要 url', path: ['url'] });
  }
});

/** 表单字段的中文显示名（Issue-2：校验错误对用户友好） */
const FIELD_ZH: Record<string, string> = {
  id: 'ID',
  name: '名称',
  transport: '传输类型',
  command: '命令',
  args: '参数',
  env: '环境变量',
  cwd: '工作目录',
  url: 'URL',
  headers: '请求头',
  gatewayMode: '网关模式',
  enabled: '启用',
  toolOverrides: '工具开关',
  serverIds: '包含的 server',
  agentTypes: '关联的 agent',
};

function formatZodIssues(error: z.ZodError): string {
  return error.issues
    .map((issue) => {
      const field = issue.path.join('.');
      const zh = FIELD_ZH[field] ?? field ?? '';
      let msg = issue.message;
      if (issue.code === 'too_small' && (issue as { type?: string }).type === 'string') msg = '不能为空';
      else if (issue.code === 'invalid_string') msg = '格式不正确';
      else if (issue.code === 'invalid_type') msg = '类型不正确';
      return zh !== '' ? `${zh}：${msg}` : msg;
    })
    .join('；');
}

export function registerApiRoutes(router: Router, daemon: Daemon): void {
  const save = () => daemon.saveConfig();

  router.get('/api/status', async (ctx) => {
    await daemon.agentDetections();
    sendJson(ctx.res, 200, {
      version: version(),
      uptimeSec: daemon.uptimeSec(),
      homeDir: daemon.store.homeDir,
      port: daemon.actualPort,
      configuredPort: daemon.config.settings.port,
      gateway: daemon.gatewayStatus(),
      serverCount: daemon.config.servers.length,
      bindingCount: daemon.config.bindings.length,
      groupCount: daemon.config.groups.length,
      resyncPending: daemon.config.settings.tokenRotatedAt !== undefined,
      authRequired: daemon.config.settings.authRequired === true,
    });
  });

  // D7：公开探测端点（免鉴权，由 daemon.handle 放行）——UI 用它决定是否显示令牌门
  router.get('/api/auth/info', async (ctx) => {
    sendJson(ctx.res, 200, { authRequired: daemon.config.settings.authRequired === true });
  });

  // M2.2：本机 server 运行时探测
  router.get('/api/environment', async (ctx) => {
    sendJson(ctx.res, 200, { tools: await detectEnvironment() });
  });

  router.get('/api/agents', async (ctx) => {
    const detections = await daemon.agentDetections(ctx.query.get('refresh') === '1');
    const byType = new Map(detections.map((d) => [d.agentType, d]));
    sendJson(ctx.res, 200, {
      agents: detections.map((d) => {
        const direct = daemon.config.bindings
          .filter((b) => b.agentType === d.agentType)
          .map((b) => b.serverId);
        const groupIds = daemon.config.groups
          .filter((g) => g.agentTypes.includes(d.agentType))
          .map((g) => g.id);
        return {
          ...d,
          syncState: daemon.config.syncState[d.agentType] ?? null,
          directServerIds: direct,
          groupIds,
          // 有效绑定 = 直连 ∪ 分组（供旧 UI/脚本兼容）
          boundServerIds: [...effectiveBoundIds(daemon.config, d.agentType)],
        };
      }),
      types: [...byType.keys()],
    });
  });

  // —— 注册表 CRUD ——
  // S4：env/headers 静态加密（存时加密、读时解密；保护 config.json 落盘）
  const decryptForDisplay = async (def: ServerDef): Promise<ServerDef> => ({
    ...def,
    env: { ...(await decryptRecord(def.env)) },
    headers: { ...(await decryptRecord(def.headers)) },
  });

  router.get('/api/servers', async (ctx) => {
    const servers = await Promise.all(daemon.config.servers.map((s) => decryptForDisplay(s)));
    sendJson(ctx.res, 200, { servers });
  });

  router.post('/api/servers', async (ctx) => {
    const parsed = serverInputSchema.safeParse(await ctx.body());
    if (!parsed.success) return sendError(ctx.res, 400, formatZodIssues(parsed.error));
    const input = parsed.data as UpsertServerInput;
    input.env = await encryptRecord(input.env);
    input.headers = await encryptRecord(input.headers);
    const def = upsertServer(daemon.config, input);
    await save();
    sendJson(ctx.res, 201, { server: await decryptForDisplay(def) });
  });

  router.get('/api/servers/:id', async (ctx) => {
    const def = daemon.config.servers.find((s) => s.id === ctx.params['id']);
    if (!def) return sendError(ctx.res, 404, 'server 不存在');
    sendJson(ctx.res, 200, { server: await decryptForDisplay(def) });
  });

  router.patch('/api/servers/:id', async (ctx) => {
    const id = ctx.params['id']!;
    const def = daemon.config.servers.find((s) => s.id === id);
    if (!def) return sendError(ctx.res, 404, 'server 不存在');
    const parsed = serverInputObject.partial().safeParse(await ctx.body());
    if (!parsed.success) return sendError(ctx.res, 400, formatZodIssues(parsed.error));
    const input = parsed.data as UpsertServerInput;
    input.env = await encryptRecord(input.env);
    input.headers = await encryptRecord(input.headers);
    upsertServer(daemon.config, { ...input, id, name: input.name ?? def.name, transport: input.transport ?? def.transport });
    await save();
    sendJson(ctx.res, 200, { server: await decryptForDisplay(daemon.config.servers.find((s) => s.id === id)!) });
  });

  router.delete('/api/servers/:id', async (ctx) => {
    const id = ctx.params['id']!;
    const def = daemon.config.servers.find((s) => s.id === id);
    if (!def) return sendError(ctx.res, 404, 'server 不存在');
    deleteServer(daemon.config, id);
    await save();
    sendJson(ctx.res, 200, { ok: true });
  });

  // —— 绑定 ——
  router.put('/api/bindings/:agentType/:serverId', async (ctx) => {
    const agentType = ctx.params['agentType']!;
    const serverId = ctx.params['serverId']!;
    try {
      setBinding(daemon.config, serverId, agentType as AgentType);
    } catch (err) {
      return sendError(ctx.res, 404, String((err as Error).message));
    }
    await save();
    sendJson(ctx.res, 200, { ok: true });
  });

  router.delete('/api/bindings/:agentType/:serverId', async (ctx) => {
    const agentType = ctx.params['agentType']!;
    const serverId = ctx.params['serverId']!;
    removeBinding(daemon.config, serverId, agentType as AgentType);
    await save();
    sendJson(ctx.res, 200, { ok: true });
  });

  // —— 分组（批量分发：分组包含 server，agent 关联分组；另支持直连绑定） ——
  const groupInputSchema = z.object({
    name: z.string().min(1, '分组名不能为空'),
    serverIds: z.array(z.string()).optional(),
    agentTypes: z.array(z.enum(['claude-code', 'codex', 'cursor', 'trae', 'opencode', 'claude-desktop', 'windsurf', 'gemini-cli'])).optional(),
  });

  router.get('/api/groups', async (ctx) => {
    sendJson(ctx.res, 200, { groups: daemon.config.groups });
  });

  router.post('/api/groups', async (ctx) => {
    const parsed = groupInputSchema.safeParse(await ctx.body());
    if (!parsed.success) return sendError(ctx.res, 400, formatZodIssues(parsed.error));
    const group = upsertGroup(daemon.config, parsed.data);
    await save();
    sendJson(ctx.res, 201, { group });
  });

  router.patch('/api/groups/:id', async (ctx) => {
    const id = ctx.params['id']!;
    const group = daemon.config.groups.find((g) => g.id === id);
    if (!group) return sendError(ctx.res, 404, '分组不存在');
    const parsed = groupInputSchema.partial().safeParse(await ctx.body());
    if (!parsed.success) return sendError(ctx.res, 400, formatZodIssues(parsed.error));
    const updated = upsertGroup(daemon.config, {
      id,
      name: parsed.data.name ?? group.name,
      serverIds: parsed.data.serverIds ?? group.serverIds,
      agentTypes: parsed.data.agentTypes ?? group.agentTypes,
    });
    await save();
    sendJson(ctx.res, 200, { group: updated });
  });

  router.delete('/api/groups/:id', async (ctx) => {
    const id = ctx.params['id']!;
    if (!daemon.config.groups.some((g) => g.id === id)) return sendError(ctx.res, 404, '分组不存在');
    deleteGroup(daemon.config, id);
    await save();
    sendJson(ctx.res, 200, { ok: true });
  });

  router.put('/api/groups/:id/servers/:serverId', async (ctx) => {
    try {
      groupAddServer(daemon.config, ctx.params['id']!, ctx.params['serverId']!);
    } catch (err) {
      return sendError(ctx.res, 404, String((err as Error).message));
    }
    await save();
    sendJson(ctx.res, 200, { ok: true });
  });

  router.delete('/api/groups/:id/servers/:serverId', async (ctx) => {
    try {
      groupRemoveServer(daemon.config, ctx.params['id']!, ctx.params['serverId']!);
    } catch (err) {
      return sendError(ctx.res, 404, String((err as Error).message));
    }
    await save();
    sendJson(ctx.res, 200, { ok: true });
  });

  router.put('/api/groups/:id/agents/:agentType', async (ctx) => {
    try {
      groupBindAgent(daemon.config, ctx.params['id']!, ctx.params['agentType']! as AgentType);
    } catch (err) {
      return sendError(ctx.res, 404, String((err as Error).message));
    }
    await save();
    sendJson(ctx.res, 200, { ok: true });
  });

  router.delete('/api/groups/:id/agents/:agentType', async (ctx) => {
    try {
      groupUnbindAgent(daemon.config, ctx.params['id']!, ctx.params['agentType']! as AgentType);
    } catch (err) {
      return sendError(ctx.res, 404, String((err as Error).message));
    }
    await save();
    sendJson(ctx.res, 200, { ok: true });
  });

  // —— 分发 ——
  router.post('/api/sync', async (ctx) => {
    const body = (await ctx.body()) as { dryRun?: boolean; resolutions?: Record<string, 'override' | 'skip'> };
    const report = await daemon.sync.sync(daemon.config, {
      dryRun: body.dryRun === true,
      resolutions: body.resolutions,
    });
    if (body.dryRun !== true) await save();
    sendJson(ctx.res, 200, { report });
  });

  // —— 导入 ——
  router.get('/api/import/preview', async (ctx) => {
    const groups = await importPreview();
    // 标注已存在于注册表的指纹
    const existing = new Set(
      daemon.config.servers.map((s) =>
        s.transport === 'stdio' ? `stdio|${s.command}|${(s.args ?? []).join(' ')}` : `${s.transport}|${s.url}`,
      ),
    );
    sendJson(ctx.res, 200, {
      groups: groups.map((g) => ({ ...g, alreadyImported: existing.has(g.fingerprint) })),
    });
  });

  router.post('/api/import', async (ctx) => {
    const body = (await ctx.body()) as { fingerprints?: string[] };
    const fingerprints = new Set(body.fingerprints ?? []);
    const groups = (await importPreview()).filter((g) => fingerprints.has(g.fingerprint));
    const added = applyImport(daemon.config, groups);
    await save();
    sendJson(ctx.res, 200, { added });
  });

  // —— 进程与工具（M1.5 网关注入实现；此前 503） ——
  router.add('POST', '/api/servers/:id/start', async (ctx) => controlUpstream(daemon, ctx, 'start'));
  router.add('POST', '/api/servers/:id/stop', async (ctx) => controlUpstream(daemon, ctx, 'stop'));
  router.add('POST', '/api/servers/:id/restart', async (ctx) => controlUpstream(daemon, ctx, 'restart'));
  router.get('/api/servers/:id/tools', async (ctx) => {
    const id = ctx.params['id']!;
    if (!daemon.gatewayControl) return sendError(ctx.res, 503, 'gateway 未启用', 'GATEWAY_DOWN');
    // 默认读缓存（连接建立过即秒回）；?refresh=1 强制重新抓取
    const tools = await daemon.probeTools?.(id, ctx.query.get('refresh') === '1');
    if (!tools) return sendError(ctx.res, 502, '无法连接上游或工具列表为空');
    sendJson(ctx.res, 200, { tools });
  });
  router.get('/api/servers/:id/logs', async (ctx) => {
    const id = ctx.params['id']!;
    sendJson(ctx.res, 200, { lines: daemon.upstreamLogs?.(id) ?? [] });
  });

  // M3.1 调试台（S6）：不经 agent 直接调用工具
  router.post('/api/servers/:id/invoke', async (ctx) => {
    const id = ctx.params['id']!;
    const body = (await ctx.body()) as { tool?: string; arguments?: Record<string, unknown> };
    if (!body.tool) return sendError(ctx.res, 400, '缺少 tool');
    if (!daemon.config.servers.some((s) => s.id === id)) {
      return sendError(ctx.res, 404, 'server 不存在');
    }
    if (daemon.invokeTool === undefined) return sendError(ctx.res, 503, 'gateway 未启用', 'GATEWAY_DOWN');
    try {
      const result = await daemon.invokeTool(id, body.tool, body.arguments ?? {});
      sendJson(ctx.res, 200, { result });
    } catch (err) {
      sendError(ctx.res, 502, String((err as Error).message ?? err));
    }
  });

  // —— 日志 ——
  router.get('/api/logs/calls', async (ctx) => {
    sendJson(ctx.res, 200, { calls: daemon.recentCalls?.() ?? [] });
  });

  // —— 设置 ——
  router.get('/api/settings', async (ctx) => {
    sendJson(ctx.res, 200, {
      port: daemon.config.settings.port,
      logLevel: daemon.config.settings.logLevel,
      authRequired: daemon.config.settings.authRequired === true,
    });
  });

  router.get('/api/settings/token', async (ctx) => {
    sendJson(ctx.res, 200, { token: daemon.config.settings.token });
  });

  router.patch('/api/settings', async (ctx) => {
    const body = (await ctx.body()) as { port?: number; logLevel?: string; authRequired?: boolean };
    if (body.port !== undefined) {
      if (!Number.isInteger(body.port) || body.port < 1 || body.port > 65535) {
        return sendError(ctx.res, 400, '端口非法');
      }
      daemon.config.settings.port = body.port;
    }
    if (body.logLevel !== undefined) {
      if (!['debug', 'info', 'warn', 'error'].includes(body.logLevel)) {
        return sendError(ctx.res, 400, '日志级别非法');
      }
      daemon.config.settings.logLevel = body.logLevel as 'debug' | 'info' | 'warn' | 'error';
      daemon.logger.setLevel(daemon.config.settings.logLevel);
    }
    if (body.authRequired !== undefined) {
      daemon.config.settings.authRequired = body.authRequired;
      // D7：开关切换后，已分发的网关配置形态（是否带凭证）与注册表不一致——提示重新同步
      if (daemon.config.bindings.length > 0) {
        daemon.config.settings.tokenRotatedAt = new Date().toISOString();
      }
    }
    await save();
    sendJson(ctx.res, 200, { ok: true });
  });

  router.post('/api/settings/token/rotate', async (ctx) => {
    daemon.config.settings.token = randomBytes(24).toString('hex');
    // M2.4：仅在令牌启用时标记待重同步（关闭时网关条目不含 token，轮换无分发影响）
    const authOn = daemon.config.settings.authRequired === true;
    if (authOn && daemon.config.bindings.length > 0) {
      daemon.config.settings.tokenRotatedAt = new Date().toISOString();
    }
    await save();
    sendJson(ctx.res, 200, {
      token: daemon.config.settings.token,
      resyncRequired: authOn && daemon.config.bindings.length > 0,
    });
  });

  // registry 搜索/浏览——九源可选：官方 / GitHub 镜像 / Smithery / npm（镜像回退）＋国内魔搭、百度，
  // 以及 Cline 市场、Docker 目录、mcpm 三个免鉴权聚合目录。
  // 支持分页（limit/offset）；q 为空 = 浏览模式（npm 需要关键词，其余源均可浏览）。
  // 官方源偶发忽略 search 参数 → 结果统一做相关性过滤与按名去重，不信任上游。
  // 官方条目缺安装配置（如仅 MCPB 桌面包）→ 按名回查 npm 补 npx 建议。
  const OFFICIAL_REGISTRY = 'https://registry.modelcontextprotocol.io';
  const GITHUB_REGISTRY = 'https://api.mcp.github.com';
  const SMITHERY_REGISTRY = 'https://registry.smithery.ai';
  const NPM_HOSTS = ['https://registry.npmjs.org', 'https://registry.npmmirror.com'];
  const MODELSCOPE_API = 'https://www.modelscope.cn/api/v1/dolphin/mcpServers';
  const BAIDU_MCP_API = 'https://www.mcpworld.com/api/mcp-market/servers';
  const CLINE_CATALOG = 'https://cline.github.io/marketplace/catalog.json';
  const DOCKER_CATALOG = 'https://desktop.docker.com/mcp/catalog/v2/catalog.json';
  const MCPM_REGISTRY = 'https://mcpm.sh/api/servers.json';
  const DEFAULT_PAGE_SIZE = 20;

  // 静态目录（Cline/Docker/mcpm 无服务端搜索）的进程内缓存：整包拉一次，TTL 内复用
  const catalogCache = new Map<string, { at: number; data: unknown }>();
  const fetchCatalog = async (url: string, attempts = 1): Promise<unknown> => {
    const hit = catalogCache.get(url);
    if (hit && Date.now() - hit.at < 10 * 60_000) return hit.data;
    let lastErr: unknown;
    for (let i = 0; i < attempts; i++) {
      try {
        const res = await fetch(url, { signal: AbortSignal.timeout(20_000), headers: { accept: 'application/json' } });
        if (!res.ok) throw new Error(`返回 ${res.status}`);
        const data: unknown = await res.json();
        catalogCache.set(url, { at: Date.now(), data });
        return data;
      } catch (err) {
        lastErr = err; // Docker CDN 偶发连接重置，允许重试
      }
    }
    throw lastErr instanceof Error ? lastErr : new Error(String(lastErr));
  };

  router.get('/api/registry/search', async (ctx) => {
    const q = ctx.query.get('q')?.trim() ?? '';
    const limit = Math.min(Math.max(Number.parseInt(ctx.query.get('limit') ?? '', 10) || DEFAULT_PAGE_SIZE, 1), 50);
    const offset = Math.max(Number.parseInt(ctx.query.get('offset') ?? '', 10) || 0, 0);
    const source = ctx.query.get('source') ?? 'official';
    const failures: string[] = [];

    // —— npm 搜索（优先官方源、失败回退国内镜像；相关性过滤 + 按周下载热度排序） ——
    const POPULARITY_WINDOW = 250; // npm 单次搜索上限 250：拉满后本地按热度排序再切片
    const npmSearch = async (): Promise<void> => {
      if (q === '') return; // npm 无浏览目录：空词直接返回，让上层兜底报错
      for (const host of NPM_HOSTS) {
        try {
          // 热度排序需要完整窗口：浅页拉大窗口排序后切片；深页（>250）退回上游相关序
          const deep = offset + limit > POPULARITY_WINDOW;
          const size = deep ? limit : Math.min(Math.max(offset + limit, 50), POPULARITY_WINDOW);
          const from = deep ? offset : 0;
          const res = await fetch(
            `${host}/-/v1/search?text=${encodeURIComponent(q)}&size=${size}&from=${from}`,
            { signal: AbortSignal.timeout(10_000) },
          );
          if (!res.ok) {
            failures.push(`npm(${host}) 返回 ${res.status}`);
            continue;
          }
          const data = (await res.json()) as Parameters<typeof npmSearchToSuggestions>[0] & { total?: number };
          // 先做相关性过滤再按热度排：npm 上游模糊匹配会把精确命中的同名包混入大量高下载无关包，
          // 纯热度排序会把它彻底埋掉（如 codebase-memory-mcp 被压到百位开外）
          const candidates = filterByRelevance(npmSearchToSuggestions(data), q);
          const servers = sortByPopularity(candidates).slice(offset - from, offset - from + limit);
          const total = data.total;
          sendJson(ctx.res, 200, {
            servers,
            source: 'npm',
            notes: failures,
            ...(total !== undefined ? { total, totalPages: Math.ceil(total / limit) } : {}),
            hasMore: total !== undefined ? offset + servers.length < total : servers.length >= limit,
          });
          return;
        } catch (err) {
          failures.push(`npm(${host}) 不可达: ${String((err as Error).message ?? err).slice(0, 80)}`);
        }
      }
    };

    // —— Smithery（totalCount/totalPages 齐全；上游单页上限 100，拉多页后按使用热度排序） ——
    const SMITHERY_PAGE = 100;
    const SMITHERY_WINDOW = 300; // 热度排序窗口：前 300 条内全局排序，更深页退回上游相关序
    const smitherySearch = async (): Promise<void> => {
      try {
        let merged: Parameters<typeof smitheryToSuggestions>[0]['servers'] = [];
        let total: number | undefined;
        let totalPages: number | undefined;
        if (offset + limit <= SMITHERY_WINDOW) {
          // 浅页：从第 1 页拉到覆盖 offset+limit，合并后按热度全局排序再切片
          const pagesToFetch = Math.min(Math.ceil((offset + limit) / SMITHERY_PAGE), SMITHERY_WINDOW / SMITHERY_PAGE);
          for (let p = 1; p <= pagesToFetch; p++) {
            const res = await fetch(
              `${SMITHERY_REGISTRY}/servers?${q !== '' ? `q=${encodeURIComponent(q)}&` : ''}page=${p}&pageSize=${SMITHERY_PAGE}`,
              { signal: AbortSignal.timeout(15_000), headers: { accept: 'application/json' } },
            );
            if (!res.ok) {
              failures.push(`Smithery 返回 ${res.status}`);
              break;
            }
            const data = (await res.json()) as Parameters<typeof smitheryToSuggestions>[0];
            merged = merged.concat(data.servers ?? []);
            total = data.pagination?.totalCount;
            totalPages = data.pagination?.totalPages;
            if (totalPages !== undefined && p >= totalPages) break;
          }
        } else {
          // 深页：退回上游相关序（单页直通）
          const page = Math.floor(offset / limit) + 1;
          const res = await fetch(
            `${SMITHERY_REGISTRY}/servers?${q !== '' ? `q=${encodeURIComponent(q)}&` : ''}page=${page}&pageSize=${limit}`,
            { signal: AbortSignal.timeout(15_000), headers: { accept: 'application/json' } },
          );
          if (!res.ok) {
            failures.push(`Smithery 返回 ${res.status}`);
          } else {
            const data = (await res.json()) as Parameters<typeof smitheryToSuggestions>[0];
            merged = data.servers ?? [];
            total = data.pagination?.totalCount;
            totalPages = data.pagination?.totalPages;
          }
        }
        if (merged.length === 0 && total === undefined) {
          await npmSearch();
          return;
        }
        const sorted = sortByPopularity(smitheryToSuggestions({ servers: merged }));
        const servers =
          offset + limit <= SMITHERY_WINDOW ? sorted.slice(offset, offset + limit) : sorted.slice(0, limit);
        sendJson(ctx.res, 200, {
          servers,
          source: 'smithery',
          notes: failures,
          ...(total !== undefined ? { total } : {}),
          ...(total !== undefined ? { totalPages: Math.ceil(total / limit) } : {}),
          hasMore: total !== undefined ? offset + servers.length < total : servers.length >= limit,
        });
      } catch (err) {
        failures.push(`Smithery 不可达: ${String((err as Error).message ?? err).slice(0, 80)}`);
        await npmSearch();
      }
    };

    // —— 官方 registry / GitHub 镜像（同构 schema：offset 翻页 + 相关性过滤 + 按名去重；失败回退） ——
    const officialBase = daemon.config.settings.registryBaseUrl?.trim() || OFFICIAL_REGISTRY;
    // npm manifest 精确回查（GET /<pkg>/latest）：官方条目缺安装配置时（如仅提供 MCPB 桌面包）
    // 按名找同名 npm 包补 npx 建议；npmjs 失败回退国内镜像。拿到 manifest 即止（镜像内容一致）。
    const npmManifestBackfill = async (
      names: string[],
    ): Promise<Map<string, NonNullable<ReturnType<typeof npmManifestToSuggestion>>>> => {
      const out = new Map<string, NonNullable<ReturnType<typeof npmManifestToSuggestion>>>();
      await Promise.all(
        names.map(async (name) => {
          for (const host of NPM_HOSTS) {
            try {
              const res = await fetch(`${host}/${encodeURIComponent(name)}/latest`, {
                signal: AbortSignal.timeout(8_000),
                headers: { accept: 'application/json' },
              });
              if (!res.ok) continue;
              const manifest = (await res.json()) as Parameters<typeof npmManifestToSuggestion>[0];
              const suggestion = npmManifestToSuggestion(manifest);
              if (suggestion !== null) out.set(name, suggestion);
              return;
            } catch {
              // 网络失败 → 换下一个镜像
            }
          }
        }),
      );
      return out;
    };
    const officialLikeSearch = async (
      base: string,
      label: string,
      source: 'official' | 'github',
      onFail: () => Promise<void>,
    ): Promise<void> => {
      try {
        // 官方是 cursor 翻页：一次拉到 offset+limit 再切片；搜索时多拉留出去重/过滤余量（上游单页上限约 200）
        const headroom = q !== '' ? limit * 2 : 0;
        const fetchLimit = Math.min(offset + limit + headroom, 200);
        const searchPart = q !== '' ? `search=${encodeURIComponent(q)}&` : '';
        const res = await fetch(
          `${base}/v0/servers?${searchPart}limit=${fetchLimit}`,
          { signal: AbortSignal.timeout(15_000), headers: { accept: 'application/json' } },
        );
        if (!res.ok) {
          failures.push(`${label} 返回 ${res.status}`);
          await onFail();
          return;
        }
        const data = (await res.json()) as {
          servers?: Array<Record<string, unknown>>;
          metadata?: { total?: number; nextCursor?: string };
        };
        const entries = (data.servers ?? []).map((e) => (e['server'] ?? e) as Record<string, unknown>);
        let candidates = dedupeSuggestions(
          entries.map((e) => registryEntryToServerInput(e)).map((s) => ({ ...s, source })),
        );
        if (q !== '') candidates = filterByRelevance(candidates, q);
        const total = data.metadata?.total;
        const page = candidates.slice(offset, offset + limit);
        // 页内缺安装配置的条目 → 按名回查 npm 补 npx 建议；只补当前页以控制外呼量
        const missing = page.filter((s) => s.suggestion === null).map((s) => s.name);
        if (missing.length > 0) {
          const backfilled = await npmManifestBackfill(missing);
          for (const s of page) {
            const fb = backfilled.get(s.name);
            if (fb === undefined) continue;
            s.suggestion = { ...fb, name: s.name }; // 展示名跟官方条目；npx 参数用 npm 包名
            s.description = [s.description, '官方条目无安装配置，已按同名 npm 包回退']
              .filter((x) => x !== '')
              .join(' · ');
          }
        }
        const hasMore =
          total !== undefined
            ? offset + page.length < total
            : candidates.length >= offset + limit || entries.length >= fetchLimit;
        return sendJson(ctx.res, 200, {
          servers: page,
          source,
          ...(total !== undefined ? { total, totalPages: Math.ceil(total / limit) } : {}),
          hasMore,
        });
      } catch (err) {
        failures.push(`${label} 不可达: ${String((err as Error).message ?? err).slice(0, 80)}`);
        await onFail();
      }
    };
    const officialSearch = (): Promise<void> =>
      officialLikeSearch(officialBase, '官方 registry', 'official', npmSearch);
    const githubSearch = (): Promise<void> =>
      officialLikeSearch(GITHUB_REGISTRY, 'GitHub registry', 'github', officialSearch);

    // —— 魔搭 ModelScope MCP 广场（国内源：PUT + JSON body，PageNumber 从 1 起；上游已做相关序） ——
    const modelscopeSearch = async (): Promise<void> => {
      try {
        const pageSize = Math.min(Math.max(offset + limit, 20), 100);
        const pageNumber = Math.floor(offset / pageSize) + 1;
        const res = await fetch(MODELSCOPE_API, {
          method: 'PUT',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ PageSize: pageSize, PageNumber: pageNumber, Query: q, Criterion: [] }),
          signal: AbortSignal.timeout(15_000),
        });
        if (!res.ok) throw new Error(`返回 ${res.status}`);
        const data = (await res.json()) as Parameters<typeof modelscopeToSuggestions>[0];
        // 上游可能返回同名的多个托管实例，按展示名去重避免 UI 重复卡片
        const candidates = dedupeSuggestions(modelscopeToSuggestions(data));
        const from = offset - (pageNumber - 1) * pageSize;
        const servers = candidates.slice(from, from + limit);
        const total = data.Data?.McpServer?.TotalCount;
        return sendJson(ctx.res, 200, {
          servers,
          source: 'modelscope',
          ...(total !== undefined ? { total, totalPages: Math.ceil(total / limit) } : {}),
          hasMore: total !== undefined ? offset + servers.length < total : servers.length >= limit,
        });
      } catch (err) {
        failures.push(`魔搭 ModelScope 不可达: ${String((err as Error).message ?? err).slice(0, 80)}`);
      }
    };

    // —— 百度 MCP World（国内源：GET，pn 从 0 起；仅元数据无安装配置 → 建议为 null 只做发现） ——
    const baiduSearch = async (): Promise<void> => {
      try {
        const params = new URLSearchParams({
          wd: q,
          type: q === '' ? 'tag' : 'search',
          pn: String(Math.floor(offset / limit)),
          lg: 'zh',
          pl: String(limit),
        });
        const res = await fetch(`${BAIDU_MCP_API}?${params}`, {
          signal: AbortSignal.timeout(15_000),
          headers: { accept: 'application/json' },
        });
        if (!res.ok) throw new Error(`返回 ${res.status}`);
        const data = (await res.json()) as Parameters<typeof baiduMcpWorldToSuggestions>[0];
        const servers = dedupeSuggestions(baiduMcpWorldToSuggestions(data));
        const total = baiduMcpWorldTotal(data);
        return sendJson(ctx.res, 200, {
          servers,
          source: 'baidu',
          ...(total !== undefined ? { total, totalPages: Math.ceil(total / limit) } : {}),
          hasMore: total !== undefined ? offset + servers.length < total : servers.length >= limit,
        });
      } catch (err) {
        failures.push(`百度 MCP World 不可达: ${String((err as Error).message ?? err).slice(0, 80)}`);
      }
    };

    // —— 静态目录三源（Cline/Docker/mcpm：整包拉取 + 进程内缓存，本地过滤分页） ——
    const catalogSearch = async (
      url: string,
      source: 'cline' | 'docker' | 'mcpm',
      label: string,
      map: (data: never) => ReturnType<typeof clineCatalogToSuggestions>,
      attempts = 1,
    ): Promise<void> => {
      try {
        const data = await fetchCatalog(url, attempts);
        let candidates = map(data as never);
        if (q !== '') candidates = filterByRelevance(candidates, q);
        const servers = candidates.slice(offset, offset + limit);
        return sendJson(ctx.res, 200, {
          servers,
          source,
          total: candidates.length,
          totalPages: Math.max(1, Math.ceil(candidates.length / limit)),
          hasMore: offset + servers.length < candidates.length,
        });
      } catch (err) {
        failures.push(`${label} 不可达: ${String((err as Error).message ?? err).slice(0, 80)}`);
      }
    };
    const clineSearch = (): Promise<void> =>
      catalogSearch(CLINE_CATALOG, 'cline', 'Cline 市场', (d) => clineCatalogToSuggestions(d));
    const dockerSearch = (): Promise<void> =>
      catalogSearch(DOCKER_CATALOG, 'docker', 'Docker MCP 目录', (d) => dockerCatalogToSuggestions(d), 2);
    const mcpmSearch = (): Promise<void> =>
      catalogSearch(MCPM_REGISTRY, 'mcpm', 'mcpm 注册表', (d) => mcpmRegistryToSuggestions(d));

    if (q === '' && source === 'npm') {
      return sendError(ctx.res, 400, 'npm 源需要搜索关键词（npm 没有可浏览的 server 目录）。请输入关键词，或选择其他源。');
    }
    if (source === 'smithery') return void smitherySearch();
    if (source === 'npm') return void npmSearch();
    if (source === 'modelscope') return void modelscopeSearch();
    if (source === 'baidu') return void baiduSearch();
    if (source === 'cline') return void clineSearch();
    if (source === 'docker') return void dockerSearch();
    if (source === 'mcpm') return void mcpmSearch();
    if (source === 'github') return void githubSearch();
    await officialSearch();
    // 各源失败时内部已尝试回退；全部失败则在此兜底
    if (!ctx.res.writableEnded) {
      sendError(
        ctx.res,
        502,
        q === ''
          ? `无法从 ${source} 源获取列表（${failures.join('；')}）。可稍后重试，或切换其他数据源。`
          : `所有数据源均不可达（${failures.join('；')}）。国际源可能需要网络代理，国内源请检查网络。`,
      );
    }
  });

  // —— 网关 ——
  router.get('/api/gateway', async (ctx) => {
    sendJson(ctx.res, 200, {
      ...daemon.gatewayStatus(),
      upstreams: daemon.gateway.upstreamSnapshots(),
    });
  });

  router.post('/api/gateway/start', async (ctx) => {
    if (!daemon.gatewayControl) return sendError(ctx.res, 503, 'gateway 模块未加载', 'GATEWAY_DOWN');
    sendJson(ctx.res, 200, await daemon.gatewayControl('start'));
  });

  router.post('/api/gateway/stop', async (ctx) => {
    if (!daemon.gatewayControl) return sendError(ctx.res, 503, 'gateway 模块未加载', 'GATEWAY_DOWN');
    sendJson(ctx.res, 200, await daemon.gatewayControl('stop'));
  });

  // —— 供应商切换（S1，吸收自 cc-switch 的投影语义）——
  const providerUpsertSchema = z.object({
    id: z.string().optional(),
    app: z.enum(['claude-code', 'codex', 'gemini-cli']),
    name: z.string().min(1),
    category: z.string().optional(),
    websiteUrl: z.string().optional(),
    fromPreset: z.string().optional(),
    config: z.object({
      env: z.record(z.string()).optional(),
      auth: z.record(z.unknown()).optional(),
      configToml: z.string().optional(),
    }),
  });
  const providerFromPresetSchema = z.object({
    app: z.enum(['claude-code', 'codex', 'gemini-cli']),
    presetName: z.string().min(1),
    apiKey: z.string().optional(),
  });

  router.get('/api/providers', async (ctx) => {
    sendJson(ctx.res, 200, { apps: daemon.providers.list() });
  });

  router.get('/api/providers/presets', async (ctx) => {
    const app = (ctx.query.get('app') ?? '') as ProviderApp;
    if (app !== 'claude-code' && app !== 'codex' && app !== 'gemini-cli') {
      return sendError(ctx.res, 400, 'app 必须是 claude-code / codex / gemini-cli');
    }
    sendJson(ctx.res, 200, { presets: daemon.providers.presets(app) });
  });

  router.post('/api/providers', async (ctx) => {
    const parsed = providerUpsertSchema.safeParse(await ctx.body());
    if (!parsed.success) return sendError(ctx.res, 400, formatZodIssues(parsed.error));
    try {
      sendJson(ctx.res, 200, { profile: await daemon.providers.upsertProfile(parsed.data) });
    } catch (err) {
      sendError(ctx.res, 400, err instanceof Error ? err.message : String(err), 'PROVIDER_SAVE_FAILED');
    }
  });

  router.post('/api/providers/from-preset', async (ctx) => {
    const parsed = providerFromPresetSchema.safeParse(await ctx.body());
    if (!parsed.success) return sendError(ctx.res, 400, formatZodIssues(parsed.error));
    try {
      const { app, presetName, apiKey } = parsed.data;
      const preset = daemon.providers.presets(app).find((p) => p.name === presetName);
      if (!preset) return sendError(ctx.res, 404, `预设不存在: ${presetName}`);
      const payload =
        app === 'codex'
          ? { auth: { ...(preset.auth ?? {}), OPENAI_API_KEY: apiKey ?? '' }, configToml: preset.config }
          : {
              env: Object.fromEntries(
                Object.entries(presetEnv(preset)).map(([k, v]) => [
                  k,
                  v === '' && apiKey !== undefined ? apiKey : v,
                ]),
              ),
            };
      const profile = await daemon.providers.upsertProfile({
        app,
        name: preset.name,
        category: preset.category,
        websiteUrl: preset.websiteUrl,
        fromPreset: preset.name,
        config: payload,
      });
      sendJson(ctx.res, 200, { profile });
    } catch (err) {
      sendError(ctx.res, 400, err instanceof Error ? err.message : String(err), 'PROVIDER_SAVE_FAILED');
    }
  });

  router.delete('/api/providers/:id', async (ctx) => {
    try {
      await daemon.providers.deleteProfile(ctx.params['id']!);
      sendJson(ctx.res, 200, { ok: true });
    } catch (err) {
      sendError(ctx.res, 400, err instanceof Error ? err.message : String(err), 'PROVIDER_DELETE_FAILED');
    }
  });

  router.post('/api/providers/:id/switch', async (ctx) => {
    const body = (await ctx.body().catch(() => ({}))) as { preserveCodexLogin?: boolean };
    try {
      sendJson(ctx.res, 200, await daemon.providers.switchTo(ctx.params['id']!, body));
    } catch (err) {
      sendError(ctx.res, 400, err instanceof Error ? err.message : String(err), 'PROVIDER_SWITCH_FAILED');
    }
  });

  // —— Skill 管理（S2，吸收自 cc-switch 的防御性设计）——
  const skillToggleSchema = z.object({
    agentType: z.string().min(1),
    enabled: z.boolean(),
  });
  const skillInstallSchema = z.object({
    repo: z.string().min(3),
    skill: z.string().optional(),
    branch: z.string().optional(),
    enableFor: z.array(z.string()).optional(),
  });
  const skillCollectionSchema = z.object({
    repo: z.string().min(3),
    branch: z.string().optional(),
    enableFor: z.array(z.string()).optional(),
  });
  const skillZipSchema = z.object({
    skill: z.string().optional(),
    collection: z.boolean().optional(),
    enableFor: z.array(z.string()).optional(),
    contentBase64: z.string().min(8),
  });
  const asSkillAgents = (list: string[] | undefined) =>
    (list ?? []).filter((a): a is SkillAgentType => SKILL_AGENT_LIST.includes(a as SkillAgentType));

  router.get('/api/skills', async (ctx) => {
    sendJson(ctx.res, 200, { skills: await daemon.skills.list(), ssotDir: daemon.skills.skillsDir });
  });

  router.get('/api/skills/search', async (ctx) => {
    const q = ctx.query.get('q') ?? '';
    const limit = Math.min(Number(ctx.query.get('limit') ?? 20) || 20, 50);
    const offset = Math.max(Number(ctx.query.get('offset') ?? 0) || 0, 0);
    try {
      sendJson(ctx.res, 200, await daemon.skills.search(q, limit, offset));
    } catch (err) {
      sendError(ctx.res, 502, err instanceof Error ? err.message : String(err), 'SKILLS_SH_DOWN');
    }
  });

  router.post('/api/skills/install', async (ctx) => {
    const parsed = skillInstallSchema.safeParse(await ctx.body());
    if (!parsed.success) return sendError(ctx.res, 400, formatZodIssues(parsed.error));
    try {
      const row = await daemon.skills.installFromRepo(parsed.data.repo, {
        skill: parsed.data.skill,
        branch: parsed.data.branch,
        enableFor: asSkillAgents(parsed.data.enableFor),
      });
      sendJson(ctx.res, 200, { skill: row });
    } catch (err) {
      sendError(ctx.res, 400, err instanceof Error ? err.message : String(err), 'SKILL_INSTALL_FAILED');
    }
  });

  // 集合安装：一次下载仓库归档，收编全部含 SKILL.md 的目录；单技能冲突跳过不中断
  router.post('/api/skills/install-collection', async (ctx) => {
    const parsed = skillCollectionSchema.safeParse(await ctx.body());
    if (!parsed.success) return sendError(ctx.res, 400, formatZodIssues(parsed.error));
    try {
      const report = await daemon.skills.installCollectionFromRepo(parsed.data.repo, {
        branch: parsed.data.branch,
        enableFor: asSkillAgents(parsed.data.enableFor),
      });
      sendJson(ctx.res, 200, report);
    } catch (err) {
      sendError(ctx.res, 400, err instanceof Error ? err.message : String(err), 'SKILL_INSTALL_FAILED');
    }
  });

  router.post('/api/skills/install-zip', async (ctx) => {
    const parsed = skillZipSchema.safeParse(await ctx.body());
    if (!parsed.success) return sendError(ctx.res, 400, formatZodIssues(parsed.error));
    try {
      const zip = Buffer.from(parsed.data.contentBase64, 'base64');
      if (zip.length === 0) return sendError(ctx.res, 400, 'ZIP 内容为空');
      if (parsed.data.collection === true) {
        const report = await daemon.skills.installCollectionFromLocalZip(zip, {
          enableFor: asSkillAgents(parsed.data.enableFor),
        });
        return sendJson(ctx.res, 200, { report });
      }
      const row = await daemon.skills.installFromLocalZip(zip, {
        skill: parsed.data.skill,
        enableFor: asSkillAgents(parsed.data.enableFor),
      });
      sendJson(ctx.res, 200, { skill: row });
    } catch (err) {
      sendError(ctx.res, 400, err instanceof Error ? err.message : String(err), 'SKILL_INSTALL_FAILED');
    }
  });

  router.post('/api/skills/:id/toggle', async (ctx) => {
    const parsed = skillToggleSchema.safeParse(await ctx.body());
    if (!parsed.success) return sendError(ctx.res, 400, formatZodIssues(parsed.error));
    try {
      const result = await daemon.skills.toggle(
        ctx.params['id']!,
        parsed.data.agentType as SkillAgentType,
        parsed.data.enabled,
      );
      sendJson(ctx.res, 200, result);
    } catch (err) {
      sendError(ctx.res, 400, err instanceof Error ? err.message : String(err), 'SKILL_TOGGLE_FAILED');
    }
  });

  router.get('/api/skills/updates', async (ctx) => {
    try {
      sendJson(ctx.res, 200, await daemon.skills.checkUpdates());
    } catch (err) {
      sendError(ctx.res, 500, err instanceof Error ? err.message : String(err), 'SKILL_CHECK_FAILED');
    }
  });

  router.post('/api/skills/:id/update', async (ctx) => {
    try {
      sendJson(ctx.res, 200, { skill: await daemon.skills.update(ctx.params['id']!) });
    } catch (err) {
      sendError(ctx.res, 400, err instanceof Error ? err.message : String(err), 'SKILL_UPDATE_FAILED');
    }
  });

  router.delete('/api/skills/:id', async (ctx) => {
    try {
      sendJson(ctx.res, 200, await daemon.skills.uninstall(ctx.params['id']!));
    } catch (err) {
      sendError(ctx.res, 400, err instanceof Error ? err.message : String(err), 'SKILL_UNINSTALL_FAILED');
    }
  });
}

function controlUpstream(
  daemon: Daemon,
  ctx: Parameters<Parameters<Router['add']>[2]>[0],
  action: 'start' | 'stop' | 'restart',
): void {
  const id = ctx.params['id']!;
  if (!daemon.config.servers.some((s) => s.id === id)) {
    sendError(ctx.res, 404, 'server 不存在');
    return;
  }
  if (!daemon.upstreamControl) {
    sendError(ctx.res, 503, 'gateway 未启用', 'GATEWAY_DOWN');
    return;
  }
  void daemon
    .upstreamControl(id, action)
    .then((r) => sendJson(ctx.res, 200, r))
    .catch((err) => sendError(ctx.res, 500, String(err)));
}

function version(): string {
  return '0.1.0';
}
