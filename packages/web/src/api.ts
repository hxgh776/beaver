const TOKEN_KEY = 'beaver_token';
const LEGACY_TOKEN_KEY = 'mcpmgr_token';

export function getToken(): string {
  const current = localStorage.getItem(TOKEN_KEY);
  if (current) return current;
  // 重命名迁移：旧键里的令牌原样升级到新键，避免用户重登
  const legacy = localStorage.getItem(LEGACY_TOKEN_KEY);
  if (legacy) {
    localStorage.setItem(TOKEN_KEY, legacy);
    localStorage.removeItem(LEGACY_TOKEN_KEY);
  }
  return legacy ?? '';
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token);
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly code?: string,
  ) {
    super(message);
  }
}

async function request<T>(method: string, pathname: string, body?: unknown): Promise<T> {
  const res = await fetch(pathname, {
    method,
    headers: {
      authorization: `Bearer ${getToken()}`,
      ...(body !== undefined ? { 'content-type': 'application/json' } : {}),
    },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  const data = (await res.json().catch(() => ({}))) as Record<string, unknown>;
  if (!res.ok) {
    throw new ApiError(res.status, String(data['error'] ?? res.statusText), data['code'] as string | undefined);
  }
  return data as T;
}

export const api = {
  get: <T>(p: string) => request<T>('GET', p),
  post: <T>(p: string, body?: unknown) => request<T>('POST', p, body),
  put: <T>(p: string, body?: unknown) => request<T>('PUT', p, body),
  patch: <T>(p: string, body: unknown) => request<T>('PATCH', p, body),
  del: <T>(p: string) => request<T>('DELETE', p),
};

// —— API 类型（与 server 契约对应） ——

export interface StatusInfo {
  version: string;
  uptimeSec: number;
  homeDir: string;
  port: number;
  configuredPort: number;
  gateway: { running: boolean; port?: number };
  serverCount: number;
  bindingCount: number;
  groupCount: number;
  resyncPending: boolean;
  authRequired: boolean;
}

export interface RuntimeTool {
  name: string;
  role: string;
  found: boolean;
  version?: string;
}

export interface AgentInfo {
  agentType: string;
  displayName: string;
  /** 候选配置文件任一存在即视为已安装 */
  detected: boolean;
  configPaths: string[];
  transports: string[];
  /** 有效绑定（直连 ∪ 分组包含） */
  boundServerIds: string[];
  directServerIds: string[];
  groupIds: string[];
  syncState: { keyStates: { key: string; hash: string }[]; updatedAt: string } | null;
}

export interface GroupInfo {
  id: string;
  name: string;
  serverIds: string[];
  agentTypes: string[];
  createdAt: string;
  updatedAt: string;
}

export interface ServerDefDTO {
  id: string;
  name: string;
  transport: 'stdio' | 'http' | 'sse';
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  cwd?: string;
  url?: string;
  headers?: Record<string, string>;
  gatewayMode: boolean;
  enabled: boolean;
  toolOverrides?: Record<string, { enabled: boolean }>;
  concurrency?: number;
}

export interface RegistrySuggestion {
  name: string;
  description: string;
  suggestion: {
    name: string;
    transport: 'stdio' | 'http' | 'sse';
    command?: string;
    args?: string[];
    url?: string;
    gatewayMode: boolean;
  } | null;
  /** 结果来源（回退时可能与所选源不同） */
  source?: 'official' | 'github' | 'smithery' | 'npm' | 'modelscope' | 'baidu' | 'cline' | 'docker' | 'mcpm';
  /** 热度信号：npm=周下载量，Smithery=使用次数，魔搭=调用量，百度=star */
  useCount?: number;
}

export interface RegistrySearchResult {
  servers: RegistrySuggestion[];
  source?: 'official' | 'github' | 'smithery' | 'npm' | 'modelscope' | 'baidu' | 'cline' | 'docker' | 'mcpm';
  total?: number;
  totalPages?: number;
  hasMore?: boolean;
  notes?: string[];
}

export interface SyncReport {
  perAgent: Array<{
    agentType: string;
    file: string | null;
    changes: { key: string; action: string; serverId?: string }[];
    conflicts: { key: string; resolutionKey: string; resolution: string; serverId?: string }[];
    unsupported: { serverId: string; reason: string }[];
  }>;
}

export interface GatewayInfo {
  running: boolean;
  port?: number;
  upstreams: Array<{
    id: string;
    status: string;
    pid?: number;
    toolCount: number;
    lastError: string | null;
  }>;
}

export interface ToolInfo {
  name: string;
  description?: string;
  enabled: boolean;
}

export interface CallRecordDTO {
  ts: string;
  scope: string;
  serverId: string;
  tool: string;
  ok: boolean;
  durationMs: number;
  error?: string;
}

export interface ImportGroup {
  fingerprint: string;
  alreadyImported: boolean;
  candidates: Array<{ agentType: string; key: string }>;
  suggested: { name: string; transport: string };
}

// —— Skill 管理（S2） ——

export interface SkillProjectionStatusDTO {
  agentType: string;
  enabled: boolean;
  projected: boolean;
  modified?: boolean;
}

export interface SkillWithStatus {
  id: string;
  directory: string;
  name: string;
  description?: string;
  source: 'github' | 'skills.sh' | 'local' | 'import';
  repoOwner?: string;
  repoName?: string;
  branch?: string;
  skillId?: string;
  installs?: number;
  readmeUrl?: string;
  contentHash: string;
  installedAt: string;
  updatedAt: string;
  targets: SkillProjectionStatusDTO[];
}

export interface SkillSearchResult {
  query: string;
  count: number;
  skills: Array<{ skillId: string; name: string; installs: number; repo: string; branch: string }>;
}

export interface SkillUpdateReport {
  updates: Array<{ skillId: string; directory: string; repo: string; localHash: string; remoteHash: string; missing?: boolean }>;
  failures: Array<{ repo: string; reason: string }>;
  checkedAt: string;
}

/** 集合安装（整仓 / ZIP 多技能）结果 */
export interface SkillCollectionReport {
  installed: SkillWithStatus[];
  failed: Array<{ directory: string; reason: string }>;
}

// —— 供应商切换（S1） ——

export type ProviderApp = 'claude-code' | 'codex' | 'gemini-cli';

export interface ProviderProfileDTO {
  id: string;
  app: ProviderApp;
  name: string;
  category?: string;
  websiteUrl?: string;
  fromPreset?: string;
  createdAt: string;
  updatedAt: string;
  lastSwitchedAt?: string;
}

export interface ProviderPresetDTO {
  name: string;
  category?: string;
  websiteUrl?: string;
  isOfficial: boolean;
}
