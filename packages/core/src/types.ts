/** 领域类型——MCP Manager 单一事实来源的数据模型。 */

export const AGENT_TYPES = [
  'claude-code',
  'codex',
  'cursor',
  'trae',
  'opencode',
  'claude-desktop',
  'windsurf',
  'gemini-cli',
  'zcode',
  'workbuddy',
] as const;
export type AgentType = (typeof AGENT_TYPES)[number];

export type TransportType = 'stdio' | 'http' | 'sse';

/** MCP Server 定义——注册表中的单一事实来源。 */
export interface ServerDef {
  /** slug，同时作为写入各 agent 配置时的键名，必须满足 ^[a-z0-9][a-z0-9-]*$（TOML bare key 兼容） */
  id: string;
  name: string;
  transport: TransportType;
  /** stdio */
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  cwd?: string;
  /** http */
  url?: string;
  headers?: Record<string, string>;
  /** D5：true = 经网关分发（agent 侧只写一条指向 gateway 的配置），false = 直连分发 */
  gatewayMode: boolean;
  enabled: boolean;
  /** G4：工具级开关，键为上游工具原始名 */
  toolOverrides?: Record<string, { enabled: boolean }>;
  /** M3.2 G12：stdio 上游并发度（1=串行最安全；支持并发的 server 可调高） */
  concurrency?: number;
  createdAt: string;
  updatedAt: string;
}

/** 绑定：server × agent 的分发关系。direct/gateway 语义由 server.gatewayMode 派生。 */
export interface AgentBinding {
  serverId: string;
  agentType: AgentType;
  addedAt: string;
}

/** 分组：一批 server 打包分发给一批 agent。agent 的有效绑定 = 直连绑定 ∪ 所属分组包含的 server。 */
export interface ServerGroup {
  id: string;
  name: string;
  serverIds: string[];
  agentTypes: AgentType[];
  createdAt: string;
  updatedAt: string;
}

/** 网关聚合配置在各 agent 配置文件中占用的公共键名 */
export const GATEWAY_KEY = 'beaver-gateway';

/** 项目更名前使用的旧键名：同步时无条件清理，导入时同样视为自身条目 */
export const LEGACY_GATEWAY_KEY = 'agent-control-gateway';

/** 管辖片段（写入 agent 配置的某个键）的 hash 快照，用于 C8 冲突检测 */
export interface KeyState {
  key: string;
  hash: string;
}

export interface AgentSyncState {
  agentType: AgentType;
  keyStates: KeyState[];
  updatedAt: string;
}

export interface Settings {
  port: number;
  token: string;
  logLevel: 'debug' | 'info' | 'warn' | 'error';
  /** M2.4：token 轮换时间（ISO）。已分发的网关配置需要重新同步；网关条目重写后清除。 */
  tokenRotatedAt?: string;
  /** D7：访问令牌开关。默认 false（仅环回监听兜底）；开启后 /api 与网关要求 Bearer token。 */
  authRequired?: boolean;
  /** M3.6：官方 registry 基地址（默认 registry.modelcontextprotocol.io；可换镜像）。 */
  registryBaseUrl?: string;
}

export interface ManagerConfig {
  version: 1;
  servers: ServerDef[];
  bindings: AgentBinding[];
  groups: ServerGroup[];
  skills: import('./skills/types.js').InstalledSkill[];
  skillTargets: import('./skills/types.js').SkillTarget[];
  providers: import('./providers/types.js').ProviderProfile[];
  providerCurrent: Partial<Record<import('./providers/types.js').ProviderApp, string>>;
  syncState: Partial<Record<AgentType, AgentSyncState>>;
  settings: Settings;
}

export interface AgentDetection {
  agentType: AgentType;
  displayName: string;
  /** 候选配置文件任一存在即视为已安装（配置可能是卸载残留，但足以判定可用） */
  detected: boolean;
  /** 存在的配置文件路径；都不存在时返回全部候选路径（供 UI 展示与分发写入选址） */
  configPaths: string[];
  transports: TransportType[];
}
