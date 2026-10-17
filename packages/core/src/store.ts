import { randomBytes } from 'node:crypto';
import { promises as fs, existsSync, readdirSync, renameSync, rmdirSync } from 'node:fs';
import { homedir } from 'node:os';
import path from 'node:path';
import { z } from 'zod';
import { atomicWriteFile } from './fsutil.js';
import type { ManagerConfig } from './types.js';

const serverDefSchema = z.object({
  id: z.string().regex(/^[a-z0-9][a-z0-9-]*$/),
  name: z.string().min(1),
  transport: z.enum(['stdio', 'http', 'sse']),
  command: z.string().optional(),
  args: z.array(z.string()).optional(),
  env: z.record(z.string()).optional(),
  cwd: z.string().optional(),
  url: z.string().optional(),
  headers: z.record(z.string()).optional(),
  gatewayMode: z.boolean(),
  enabled: z.boolean(),
  toolOverrides: z.record(z.object({ enabled: z.boolean() })).optional(),
  createdAt: z.string(),
  updatedAt: z.string(),
});

const configSchema = z.object({
  version: z.literal(1),
  servers: z.array(serverDefSchema),
  bindings: z.array(
    z.object({
      serverId: z.string(),
      agentType: z.string(),
      addedAt: z.string(),
    }),
  ),
  groups: z.array(
    z.object({
      id: z.string(),
      name: z.string().min(1),
      serverIds: z.array(z.string()),
      agentTypes: z.array(z.string()),
      createdAt: z.string(),
      updatedAt: z.string(),
    }),
  ).default([]), // 旧版本配置无 groups 字段 → 迁移为空数组
  skills: z.array(
    z.object({
      id: z.string(),
      directory: z.string(),
      name: z.string(),
      description: z.string().optional(),
      source: z.enum(['github', 'skills.sh', 'local', 'import']),
      repoOwner: z.string().optional(),
      repoName: z.string().optional(),
      branch: z.string().optional(),
      skillId: z.string().optional(),
      installs: z.number().optional(),
      readmeUrl: z.string().optional(),
      contentHash: z.string(),
      installedAt: z.string(),
      updatedAt: z.string(),
    }),
  ).default([]),
  skillTargets: z.array(
    z.object({
      skillId: z.string(),
      agentType: z.string(),
      enabled: z.boolean(),
    }),
  ).default([]),
  providers: z.array(
    z.object({
      id: z.string(),
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
      createdAt: z.string(),
      updatedAt: z.string(),
      lastSwitchedAt: z.string().optional(),
    }),
  ).default([]),
  providerCurrent: z.record(z.enum(['claude-code', 'codex', 'gemini-cli']), z.string()).default({}),
  syncState: z.record(
    z.object({
      agentType: z.string(),
      keyStates: z.array(z.object({ key: z.string(), hash: z.string() })),
      updatedAt: z.string(),
    }),
  ),
  settings: z.object({
    port: z.number().int().min(1).max(65535),
    token: z.string().min(8),
    logLevel: z.enum(['debug', 'info', 'warn', 'error']),
    tokenRotatedAt: z.string().optional(),
    authRequired: z.boolean().optional(),
    registryBaseUrl: z.string().optional(),
  }),
});

export function defaultConfig(): ManagerConfig {
  return {
    version: 1,
    servers: [],
    bindings: [],
    groups: [],
    skills: [],
    skillTargets: [],
    providers: [],
    providerCurrent: {},
    syncState: {},
    settings: {
      port: 6280,
      token: randomBytes(24).toString('hex'),
      logLevel: 'info',
      authRequired: false, // D7：默认不启用令牌，仅环回监听兜底
    },
  };
}

/**
 * 数据目录解析优先级：构造参数 > BEAVER_HOME（测试沙箱，兼容旧名 AGENT_CONTROL_HOME / MCP_MANAGER_HOME）
 * > ~/.beaver（若存在旧目录 ~/.agent-control 或 ~/.mcp-manager 且新目录不存在，一次性整体迁移）
 */
export function resolveHomeDir(explicit?: string): string {
  if (explicit) return explicit;
  const env = process.env['BEAVER_HOME'] ?? process.env['AGENT_CONTROL_HOME'] ?? process.env['MCP_MANAGER_HOME'];
  if (env) return env;
  const home = homedir();
  const legacy = [path.join(home, '.agent-control'), path.join(home, '.mcp-manager')].find((p) => existsSync(p));
  return maybeMigrateHome(legacy ?? path.join(home, '.agent-control'), path.join(home, '.beaver'));
}

/** 一次性整体迁移：旧目录存在且新目录不存在（或存在但为空，如被空跑创建）→ rename；
 * 新目录已有内容或旧目录被占用（旧 daemon 在跑）→ 沿用现状，下次启动再迁 */
export function maybeMigrateHome(legacyDir: string, newDir: string): string {
  if (!existsSync(legacyDir)) return newDir;
  if (existsSync(newDir)) {
    let empty = false;
    try {
      empty = readdirSync(newDir).length === 0;
    } catch {
      return newDir;
    }
    if (!empty) return newDir;
    try {
      rmdirSync(newDir);
    } catch {
      return newDir;
    }
  }
  try {
    renameSync(legacyDir, newDir);
  } catch {
    return legacyDir; // 下次启动再迁
  }
  return newDir;
}

export class Store {
  readonly homeDir: string;

  constructor(homeDir?: string) {
    this.homeDir = resolveHomeDir(homeDir);
  }

  get configPath(): string {
    return path.join(this.homeDir, 'config.json');
  }

  get backupsDir(): string {
    return path.join(this.homeDir, 'backups');
  }

  get logsDir(): string {
    return path.join(this.homeDir, 'logs');
  }

  get pidPath(): string {
    return path.join(this.homeDir, 'daemon.pid');
  }

  /**
   * 加载配置：文件缺失 → 返回默认配置并落盘；
   * 文件损坏 → 移为 config.json.corrupt-<ts> 后重建默认配置（绝不静默丢弃用户数据之外的东西）。
   */
  async load(): Promise<ManagerConfig> {
    let text: string | null = null;
    try {
      text = await fs.readFile(this.configPath, 'utf8');
    } catch {
      text = null;
    }
    if (text === null) {
      const config = defaultConfig();
      await this.save(config);
      return config;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = undefined;
    }
    const validated = configSchema.safeParse(parsed);
    if (!validated.success) {
      await fs.rename(
        this.configPath,
        `${this.configPath}.corrupt-${Date.now()}`,
      ).catch(() => {});
      const config = defaultConfig();
      await this.save(config);
      return config;
    }
    return validated.data as ManagerConfig;
  }

  async save(config: ManagerConfig): Promise<void> {
    await atomicWriteFile(this.configPath, `${JSON.stringify(config, null, 2)}\n`);
  }
}
