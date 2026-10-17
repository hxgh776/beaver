import type { UpsertServerInput } from './registry.js';

/** M3.6 S7：官方 MCP Registry（registry.modelcontextprotocol.io）条目 → 本注册表输入的映射 */

export interface RegistryRemote {
  type?: string;
  url?: string;
}

export interface RegistryPackage {
  registryType?: string;
  registry_name?: string;
  identifier?: string;
  name?: string;
  version?: string;
  /** GitHub MCP Registry 用 runtime_hint（uvx/npx）标注运行时，registry_name 可为空 */
  runtime_hint?: string;
}

/** 发现结果来源（与 /api/registry/search 的 source 参数一致） */
export type RegistrySource =
  | 'official'
  | 'github'
  | 'smithery'
  | 'npm'
  | 'modelscope'
  | 'baidu'
  | 'cline'
  | 'docker'
  | 'mcpm';

export interface RegistryServerEntry {
  name?: string;
  description?: string;
  remotes?: RegistryRemote[];
  packages?: RegistryPackage[];
}

export interface RegistrySuggestion {
  name: string;
  description: string;
  suggestion: UpsertServerInput | null;
  /** 结果来源：official=官方 registry；github=GitHub 镜像；smithery=Smithery；npm=npm 搜索；modelscope/baidu=国内源；cline/docker/mcpm=国际聚合目录 */
  source?: RegistrySource;
  /** 各源的热度信号（npm 周下载、Smithery/魔搭使用与调用量、百度 star），用于结果排序展示 */
  useCount?: number;
}

export function registryEntryToServerInput(entry: RegistryServerEntry): RegistrySuggestion {
  const name = shortName(entry.name ?? '');
  const description = entry.description ?? '';
  // 1) 远程端点优先：streamable-http > sse
  const remote = (entry.remotes ?? []).find((r) => r.type === 'streamable-http') ??
    (entry.remotes ?? []).find((r) => r.type === 'sse') ??
    (entry.remotes ?? [])[0];
  if (remote?.url) {
    const transport = remote.type === 'sse' ? 'sse' : 'http';
    return {
      name,
      description,
      suggestion: {
        name,
        transport,
        url: remote.url,
        gatewayMode: true,
      },
    };
  }
  // 2) 包：npm → npx；pypi → uvx。GitHub MCP Registry 的 registry_name 为空、
  //    只给 runtime_hint（uvx/npx），这里一并推断，避免误判为 npm。
  const pkg = (entry.packages ?? [])[0];
  const pkgName = pkg?.identifier ?? pkg?.name;
  if (pkg !== undefined && pkgName !== undefined) {
    const registryType = (
      pkg.registryType ||
      pkg.registry_name ||
      (pkg.runtime_hint === 'uvx' || pkg.runtime_hint === 'uv' ? 'pypi' : '') ||
      'npm'
    ).toLowerCase();
    if (registryType === 'npm') {
      return {
        name,
        description,
        suggestion: { name, transport: 'stdio', command: 'npx', args: ['-y', pkgName], gatewayMode: true },
      };
    }
    if (registryType === 'pypi') {
      return {
        name,
        description,
        suggestion: { name, transport: 'stdio', command: 'uvx', args: [pkgName], gatewayMode: true },
      };
    }
  }
  return { name, description, suggestion: null };
}

/** registry 全名形如 com.github/user/pkg 或 io.github.user/pkg → 取尾段作展示名/id 基础 */
function shortName(full: string): string {
  const base = full.split('/').at(-1) ?? full;
  return base.replace(/^mcp-/, '') || full;
}

/**
 * npm 搜索回退（官方 registry API 在部分网络不可达时）：
 * registry.npmjs.org / registry.npmmirror.com 的 -/v1/search 结果 → 建议（npx 型，网关模式）。
 */
export function npmSearchToSuggestions(
  data: {
    objects?: Array<{
      package?: { name?: string; description?: string; version?: string };
      downloads?: { weekly?: number; monthly?: number };
    }>;
  },
): RegistrySuggestion[] {
  const out: RegistrySuggestion[] = [];
  for (const obj of data.objects ?? []) {
    const pkg = obj.package;
    if (pkg?.name === undefined) continue;
    out.push({
      name: pkg.name,
      description: [pkg.description, pkg.version !== undefined ? `v${pkg.version}` : '']
        .filter((s) => s !== undefined && s !== '')
        .join(' · '),
      suggestion: {
        name: pkg.name,
        transport: 'stdio',
        command: 'npx',
        args: ['-y', pkg.name],
        gatewayMode: true,
      },
      source: 'npm',
      useCount: obj.downloads?.weekly,
    });
  }
  return out;
}

/**
 * npm 包 manifest（`GET /<pkg>/latest` 按名精确回查）→ npx 建议。
 * 供官方 registry 条目缺安装配置时（如仅提供 MCPB 桌面包）按同名包回退；无 bin 的包无法经 npx 运行 → null。
 */
export interface NpmManifest {
  name?: string;
  bin?: string | Record<string, string>;
}

export function npmManifestToSuggestion(data: NpmManifest): RegistrySuggestion['suggestion'] {
  if (data.name === undefined || data.bin === undefined) return null;
  if (typeof data.bin !== 'string' && Object.keys(data.bin).length === 0) return null;
  return { name: data.name, transport: 'stdio', command: 'npx', args: ['-y', data.name], gatewayMode: true };
}

// —— Smithery（registry.smithery.ai，国际第三方大源，免 key 列表查询） ——

export interface SmitherySearchResponse {
  servers?: Array<{
    qualifiedName?: string;
    displayName?: string;
    slug?: string;
    description?: string;
    useCount?: number;
    remote?: boolean;
    homepage?: string;
  }>;
  pagination?: { currentPage?: number; totalPages?: number; totalCount?: number };
}

/**
 * Smithery 列表 → 建议：统一走 Smithery 托管代理端点（streamable-http，网关模式），
 * 认证需用户自备 smithery API key（写入 headers 占位提示）。
 */
export function smitheryToSuggestions(data: SmitherySearchResponse): RegistrySuggestion[] {
  const out: RegistrySuggestion[] = [];
  for (const s of data.servers ?? []) {
    if (s.qualifiedName === undefined) continue;
    const name = s.displayName?.trim() || s.qualifiedName.split('/').at(-1) || s.qualifiedName;
    out.push({
      name,
      description: s.description?.split('\n')[0] ?? '',
      useCount: s.useCount,
      suggestion: {
        name,
        transport: 'http',
        url: `https://server.smithery.ai/@${s.qualifiedName}/mcp`,
        headers: { Authorization: 'Bearer {smithery_api_key}' },
        gatewayMode: true,
      },
      source: 'smithery',
    });
  }
  return out;
}

// —— 魔搭 ModelScope MCP 广场（国内最大源，PUT + JSON body，免鉴权；中文描述 + 完整传输配置） ——

export interface ModelScopeMcpItem {
  Name?: string;
  ChineseName?: string;
  Abstract?: string;
  AbstractCN?: string;
  Readme?: string;
  ReadmeCN?: string;
  /** 传输配置统一挂在 mcpServers 键下：stdio 是 command/args，远程是 url（可带 type: sse） */
  ServerConfig?: Array<{ mcpServers?: Record<string, { type?: string; command?: string; args?: string[]; url?: string }> }>;
  SSEServerConfig?: Array<{ mcpServers?: Record<string, { url?: string }> }> | null;
  StreamableHTTPServerConfig?: Array<{ mcpServers?: Record<string, { url?: string }> }> | null;
  /** 托管部署端点需 ModelScope 登录态，不作为自动添加的候选 */
  Hosted?: boolean;
  DeployedUrl?: string;
  CallVolume?: number;
  Stars?: number;
}

export interface ModelScopeResponse {
  Code?: number;
  Data?: { McpServer?: { TotalCount?: number; McpServers?: ModelScopeMcpItem[] } };
}

export function modelscopeToSuggestions(data: ModelScopeResponse): RegistrySuggestion[] {
  const out: RegistrySuggestion[] = [];
  for (const item of data.Data?.McpServer?.McpServers ?? []) {
    if (item.Name === undefined) continue;
    const name = item.ChineseName?.trim() || item.Name;
    const description = (
      item.AbstractCN?.trim() ||
      item.Abstract?.trim() ||
      ((item.ReadmeCN ?? item.Readme ?? '').split('\n')[0]?.trim() ?? '')
    ).slice(0, 160);
    // 热度：调用量优先，无调用取 star
    const useCount = (item.CallVolume ?? 0) > 0 ? item.CallVolume : (item.Stars ?? 0) > 0 ? item.Stars : undefined;
    // 传输配置优先级：本地 stdio（最可靠）→ ServerConfig 远程 → 托管 streamable-http/sse
    const defs = (item.ServerConfig ?? []).flatMap((c) => Object.values(c.mcpServers ?? {}));
    const stdio = defs.find((d) => d.command !== undefined);
    const remote = defs.find((d) => d.url !== undefined);
    const http = (item.StreamableHTTPServerConfig ?? []).flatMap((c) => Object.values(c.mcpServers ?? {})).find((d) => d.url !== undefined);
    const sse = (item.SSEServerConfig ?? []).flatMap((c) => Object.values(c.mcpServers ?? {})).find((d) => d.url !== undefined);
    let suggestion: UpsertServerInput | null = null;
    if (stdio?.command !== undefined) {
      suggestion = {
        name,
        transport: 'stdio',
        command: stdio.command,
        ...(stdio.args !== undefined && stdio.args.length > 0 ? { args: stdio.args } : {}),
        gatewayMode: true,
      };
    } else if (remote?.url !== undefined) {
      suggestion = { name, transport: remote.type === 'sse' ? 'sse' : 'http', url: remote.url, gatewayMode: true };
    } else if (http?.url !== undefined) {
      suggestion = { name, transport: 'http', url: http.url, gatewayMode: true };
    } else if (sse?.url !== undefined) {
      suggestion = { name, transport: 'sse', url: sse.url, gatewayMode: true };
    }
    out.push({
      name,
      description,
      ...(useCount !== undefined ? { useCount } : {}),
      suggestion,
      source: 'modelscope',
    });
  }
  return out;
}

// —— 百度 MCP World（国内最大收录量，无鉴权；仅元数据 + 仓库链接，无安装配置 → 仅浏览发现） ——

export interface BaiduMcpItem {
  serverName?: string;
  description?: string;
  serverUrl?: string;
  star?: number;
  isHighQuality?: boolean;
}

export interface BaiduMcpWorldResponse {
  code?: number;
  /** mcpList 有三种形态：条目数组 / 包含 {query,total,servers} wrapper 的数组 / wrapper 对象本身 */
  data?: { count?: number; mcpList?: Array<BaiduMcpItem | { query?: string; total?: number; servers?: Array<BaiduMcpItem> }> | { total?: number; servers?: Array<BaiduMcpItem> } };
}

function baiduItems(data: BaiduMcpWorldResponse): Array<BaiduMcpItem> {
  const raw = data.data?.mcpList;
  const items: Array<BaiduMcpItem> = [];
  if (Array.isArray(raw)) {
    for (const x of raw) {
      if ((x as BaiduMcpItem).serverName !== undefined) items.push(x as BaiduMcpItem);
      else if (Array.isArray((x as { servers?: Array<BaiduMcpItem> }).servers)) {
        items.push(...((x as { servers: Array<BaiduMcpItem> }).servers));
      }
    }
  } else if (raw !== undefined && raw !== null && Array.isArray(raw.servers)) {
    items.push(...raw.servers);
  }
  return items;
}

/** 搜索命中总数：优先 mcpList 内层 total（搜索结果数），否则全局收录数 data.count */
export function baiduMcpWorldTotal(data: BaiduMcpWorldResponse): number | undefined {
  const raw = data.data?.mcpList;
  if (Array.isArray(raw)) {
    for (const x of raw) {
      const t = (x as { total?: number }).total;
      if (typeof t === 'number') return t;
    }
  } else if (typeof raw?.total === 'number') {
    return raw.total;
  }
  return data.data?.count;
}

export function baiduMcpWorldToSuggestions(data: BaiduMcpWorldResponse): RegistrySuggestion[] {
  const out: RegistrySuggestion[] = [];
  for (const it of baiduItems(data)) {
    if (it.serverName === undefined) continue;
    out.push({
      name: it.serverName,
      description: (it.description ?? '').slice(0, 160),
      ...(it.star !== undefined && it.star > 0 ? { useCount: it.star } : {}),
      suggestion: null,
      source: 'baidu',
    });
  }
  return out;
}

// —— Cline 市场（人工精选，静态目录；install 为 `cline mcp install …` 模板，解析出可执行配置） ——

export interface ClineCatalogEntry {
  type?: string;
  name?: string;
  tagline?: string;
  description?: string;
  install?: {
    command?: string;
    args?: string[];
    env?: Array<{ name?: string; required?: boolean; description?: string }>;
  };
}

export function clineCatalogToSuggestions(data: { entries?: ClineCatalogEntry[] }): RegistrySuggestion[] {
  const out: RegistrySuggestion[] = [];
  for (const e of data.entries ?? []) {
    if (e.type !== 'mcp' || e.name === undefined) continue;
    const args = e.install?.args ?? [];
    let suggestion: UpsertServerInput | null = null;
    // 模板两种形态：`<id> --transport http <url>` → 远程端点；`<id> -- npx -y <pkg>` → `--` 后是真实 stdio 启动命令
    const tIdx = args.indexOf('--transport');
    const sepIdx = args.indexOf('--');
    if (tIdx !== -1) {
      const url = args.slice(tIdx + 2).find((a) => /^https?:\/\//.test(a));
      if (url !== undefined) suggestion = { name: e.name, transport: 'http', url, gatewayMode: true };
    } else if (sepIdx !== -1 && args[sepIdx + 1] !== undefined) {
      const [command, ...rest] = args.slice(sepIdx + 1);
      suggestion = {
        name: e.name,
        transport: 'stdio',
        command,
        ...(rest.length > 0 ? { args: rest } : {}),
        gatewayMode: true,
      };
    }
    const requiredEnv = (e.install?.env ?? [])
      .filter((v) => v.required === true && v.name !== undefined)
      .map((v) => v.name as string);
    const description = [
      e.description?.trim() || e.tagline?.trim() || '',
      requiredEnv.length > 0 ? `需配置环境变量：${requiredEnv.join(', ')}` : '',
    ]
      .filter((s) => s !== '')
      .join(' · ');
    out.push({ name: e.name, description, suggestion, source: 'cline' });
  }
  return out;
}

// —— Docker MCP 目录（安全审核过的 OCI 镜像，需本机 Docker；stdio = docker run -i --rm <image>） ——

export interface DockerCatalogEntry {
  type?: string;
  title?: string;
  description?: string;
  image?: string;
  source?: string;
}

export function dockerCatalogToSuggestions(data: { registry?: Record<string, DockerCatalogEntry> }): RegistrySuggestion[] {
  const out: RegistrySuggestion[] = [];
  for (const [slug, e] of Object.entries(data.registry ?? {})) {
    if ((e.type !== undefined && e.type !== 'server') || e.image === undefined) continue;
    const name = e.title?.trim() || slug;
    out.push({
      name,
      description: (e.description ?? '').slice(0, 160),
      suggestion: { name, transport: 'stdio', command: 'docker', args: ['run', '-i', '--rm', e.image], gatewayMode: true },
      source: 'docker',
    });
  }
  return out;
}

// —— mcpm.sh 聚合注册表（slug → 条目的静态 JSON；installations 给出 npm/uvx 启动模板） ——

export interface McpmRegistryEntry {
  name?: string;
  display_name?: string;
  description?: string;
  installations?: Record<string, { type?: string; command?: string; args?: string[]; env?: Record<string, string> }>;
  /** 形如 arguments: { API_KEY: { required: true, ... } } 的凭据声明 */
  arguments?: Record<string, { required?: boolean }>;
}

export function mcpmRegistryToSuggestions(
  data: Record<string, McpmRegistryEntry> | { servers?: McpmRegistryEntry[] },
): RegistrySuggestion[] {
  const entries: McpmRegistryEntry[] = Array.isArray(data)
    ? data
    : Array.isArray(data.servers)
      ? data.servers
      : Object.values(data);
  const out: RegistrySuggestion[] = [];
  for (const e of entries) {
    if (e.name === undefined) continue;
    const name = e.display_name?.trim() || e.name;
    const inst = e.installations ?? {};
    // custom 模板常是 /path/to 占位，不可执行 → 只取 npm / python（uvx）安装
    const pick = inst.npm?.command !== undefined ? inst.npm : inst.python?.command !== undefined ? inst.python : undefined;
    let suggestion: UpsertServerInput | null = null;
    if (pick?.command !== undefined) {
      suggestion = {
        name,
        transport: 'stdio',
        command: pick.command,
        ...(pick.args !== undefined && pick.args.length > 0 ? { args: pick.args } : {}),
        ...(pick.env !== undefined && Object.keys(pick.env).length > 0 ? { env: pick.env } : {}),
        gatewayMode: true,
      };
    }
    const required = Object.entries(e.arguments ?? {}).filter(([, v]) => v.required === true).map(([k]) => k);
    const description = [e.description?.trim() ?? '', required.length > 0 ? `需配置：${required.join(', ')}` : '']
      .filter((s) => s !== '')
      .join(' · ');
    out.push({ name, description, suggestion, source: 'mcpm' });
  }
  return out;
}



/** 按展示名去重（大小写不敏感，保留先出现的——上游排序通常新版本在前） */
export function dedupeSuggestions(list: RegistrySuggestion[]): RegistrySuggestion[] {
  const seen = new Set<string>();
  const out: RegistrySuggestion[] = [];
  for (const s of list) {
    const key = s.name.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(s);
  }
  return out;
}

/**
 * 相关性过滤 + 排序：官方 registry 偶发忽略 search 参数返回无过滤列表，
 * 这里按关键词 token 过滤（name 命中排前、title/description 命中次之）。
 */
export function filterByRelevance(list: RegistrySuggestion[], q: string): RegistrySuggestion[] {
  const tokens = q.toLowerCase().split(/\s+/).filter(Boolean);
  if (tokens.length === 0) return list;
  const scored: Array<{ s: RegistrySuggestion; rank: number }> = [];
  for (const s of list) {
    const name = s.name.toLowerCase();
    const desc = s.description.toLowerCase();
    let rank: number | null = null;
    if (tokens.every((t) => name.includes(t))) rank = 0;
    else if (tokens.every((t) => `${name} ${desc}`.includes(t))) rank = 1;
    else if (tokens.some((t) => name.includes(t) || desc.includes(t))) rank = 2;
    if (rank !== null) scored.push({ s, rank });
  }
  return scored
    .sort((a, b) => a.rank - b.rank || (b.s.useCount ?? 0) - (a.s.useCount ?? 0))
    .map((x) => x.s);
}

/** 按热度排序（useCount 降序，npm=周下载、smithery=使用次数；无热度数据保持原序） */
export function sortByPopularity(list: RegistrySuggestion[]): RegistrySuggestion[] {
  return [...list].sort((a, b) => (b.useCount ?? 0) - (a.useCount ?? 0));
}
