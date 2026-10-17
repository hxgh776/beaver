import { promises as fs } from 'node:fs';
import { atomicWriteFile, readTextIfExists } from '../fsutil.js';
import { MalformedConfigError, WriteVerificationError } from '../errors.js';
import { finalizeKeyStates, JSON_EQUAL, planChanges } from './shared.js';
import type {
  AgentAdapter,
  ApplyOptions,
  ApplyResult,
  FragmentWrite,
  RawServerEntry,
} from './types.js';
import type { AgentType, TransportType } from '../types.js';

export interface JsonAdapterOptions {
  type: AgentType;
  displayName: string;
  transports: TransportType[];
  paths: string[];
  /** server 条目所在的容器键，如 mcpServers / mcp；支持点路径嵌套（如 ZCode 的 mcp.servers） */
  containerKey: string;
  normalize: (raw: unknown) => RawServerEntry | null;
  denormalize: (entry: RawServerEntry) => Record<string, unknown>;
}

/**
 * 通用 JSON 容器适配器：claude-code / cursor / trae / zcode 共用。
 *
 * 写入流程（C6 安全写入）：
 * parse → 只改容器内管辖键 → 原子写 → 复读深比较校验（管辖子树之外全等）→ 失败即回滚并抛错。
 */
export class JsonAgentAdapter implements AgentAdapter {
  readonly type: AgentType;
  readonly displayName: string;
  readonly transports: TransportType[];
  readonly containerKey: string;
  private readonly paths: string[];
  private readonly segments: string[];
  private readonly normalizeFn: (raw: unknown) => RawServerEntry | null;
  private readonly denormalizeFn: (entry: RawServerEntry) => Record<string, unknown>;

  constructor(opts: JsonAdapterOptions) {
    this.type = opts.type;
    this.displayName = opts.displayName;
    this.transports = opts.transports;
    this.paths = opts.paths;
    this.containerKey = opts.containerKey;
    this.segments = opts.containerKey.split('.');
    this.normalizeFn = opts.normalize;
    this.denormalizeFn = opts.denormalize;
  }

  candidatePaths(): string[] {
    return [...this.paths];
  }

  normalizeEntry(raw: unknown): RawServerEntry | null {
    return this.normalizeFn(raw);
  }

  denormalizeEntry(entry: RawServerEntry): Record<string, unknown> {
    return this.denormalizeFn(entry);
  }

  /** 容器叶子所在的父对象与键名；中间层不是对象时返回 null（读视为无容器，写则需先创建） */
  private locate(record: Record<string, unknown>): { parent: Record<string, unknown>; leaf: string } | null {
    let cur = record;
    for (let i = 0; i < this.segments.length - 1; i++) {
      const next = cur[this.segments[i]!];
      if (next === null || typeof next !== 'object' || Array.isArray(next)) return null;
      cur = next as Record<string, unknown>;
    }
    return { parent: cur, leaf: this.segments[this.segments.length - 1]! };
  }

  /** 逐层创建中间对象并返回容器叶子位置（仅写路径使用） */
  private ensureLocate(record: Record<string, unknown>): { parent: Record<string, unknown>; leaf: string } {
    let cur = record;
    for (let i = 0; i < this.segments.length - 1; i++) {
      const seg = this.segments[i]!;
      const next = cur[seg];
      if (next === null || typeof next !== 'object' || Array.isArray(next)) {
        const fresh: Record<string, unknown> = {};
        cur[seg] = fresh;
        cur = fresh;
      } else {
        cur = next as Record<string, unknown>;
      }
    }
    return { parent: cur, leaf: this.segments[this.segments.length - 1]! };
  }

  private containerOf(record: Record<string, unknown>): Record<string, unknown> | null {
    const loc = this.locate(record);
    if (!loc) return null;
    const v = loc.parent[loc.leaf];
    return v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
  }

  async read(file: string): Promise<Map<string, RawServerEntry>> {
    const text = await readTextIfExists(file);
    if (text === null) return new Map();
    const obj = this.parseOrThrow(text, file);
    const container = this.containerOf(obj);
    const out = new Map<string, RawServerEntry>();
    if (container === null) return out;
    for (const [key, value] of Object.entries(container)) {
      const normalized = this.normalizeFn(value);
      if (normalized) out.set(key, normalized);
    }
    return out;
  }

  async apply(file: string, writes: FragmentWrite[], options: ApplyOptions): Promise<ApplyResult> {
    const text = await readTextIfExists(file);
    const obj = text === null ? {} : this.parseOrThrow(text, file);
    if (typeof obj !== 'object' || Array.isArray(obj)) {
      throw new MalformedConfigError(file);
    }
    const record = obj as Record<string, unknown>;
    // 中间层存在但不是纯对象（如 mcp: "str"）→ 形状未知，拒绝写入而非静默覆盖
    let walk: Record<string, unknown> = record;
    for (let i = 0; i < this.segments.length - 1; i++) {
      const next = walk[this.segments[i]!];
      if (next === undefined) break;
      if (next === null || typeof next !== 'object' || Array.isArray(next)) {
        throw new MalformedConfigError(file);
      }
      walk = next as Record<string, unknown>;
    }
    const existingContainer = this.containerOf(record);
    const leafValue = this.locate(record)?.parent[this.segments[this.segments.length - 1]!];
    const containerExisted = leafValue !== undefined;
    if (containerExisted && existingContainer === null) {
      throw new MalformedConfigError(file);
    }
    const container: Record<string, unknown> = existingContainer !== null
      ? { ...existingContainer }
      : {};

    const currentValues = new Map(Object.entries(container));
    const resolutions = options.resolutions ?? {};
    const { plan, conflicts } = planChanges({
      currentValues,
      writes,
      previous: options.previous,
      resolutions,
      denormalize: this.denormalizeFn,
    });
    const changes = plan.map(({ key, action }) => ({ key, action }));
    const mutations = plan.filter((p) => p.action !== 'none');

    if (options.dryRun) {
      const projected = new Map(currentValues);
      for (const m of mutations) {
        if (m.action === 'upsert') projected.set(m.key, m.desired);
        else projected.delete(m.key);
      }
      return { changes, keyStates: finalizeKeyStates(plan, projected, options.previous), conflicts };
    }

    if (mutations.length === 0) {
      return { changes, keyStates: finalizeKeyStates(plan, currentValues, options.previous), conflicts };
    }

    if (options.backup) await options.backup();

    for (const m of mutations) {
      if (m.action === 'upsert') container[m.key] = m.desired;
      else delete container[m.key];
    }
    if (Object.keys(container).length > 0 || containerExisted) {
      const loc = this.ensureLocate(record);
      loc.parent[loc.leaf] = container;
    }

    const newText = `${JSON.stringify(record, null, 2)}\n`;
    // S0：发布前落意图，发布/回滚后清账
    if (options.journal) await options.journal.record(file, text, newText);
    await atomicWriteFile(file, newText);

    // 复读校验：整体内容一致 + 屏蔽管辖子树后与写前全等
    const reread = await readTextIfExists(file);
    let verified = false;
    if (reread !== null) {
      try {
        const reparsed = JSON.parse(reread);
        if (JSON_EQUAL(reparsed, record)) {
          const snapshot = text === null ? {} : this.parseOrThrow(text, file);
          verified = JSON_EQUAL(this.maskManaged(snapshot as Record<string, unknown>), this.maskManaged(reparsed as Record<string, unknown>));
        }
      } catch {
        verified = false;
      }
    }
    if (!verified) {
      if (text === null) await fs.rm(file, { force: true });
      else await atomicWriteFile(file, text);
      if (options.journal) await options.journal.complete(file); // 已回滚到写前状态，意图清账
      throw new WriteVerificationError(file);
    }
    if (options.journal) await options.journal.complete(file);

    return {
      changes,
      keyStates: finalizeKeyStates(
        plan,
        new Map(Object.entries(this.containerOf(record) ?? {})),
        options.previous,
      ),
      conflicts,
    };
  }

  /** 深拷贝并移除管辖容器子树——剩余部分必须与写前全等。
   * 写入可能新建中间层（如首次分发创建 mcp），摘除叶子后残留的空中间壳
   * 两侧对称剪除，写前不含该中间层的快照才能与写后对齐。 */
  private maskManaged(record: Record<string, unknown>): Record<string, unknown> {
    const clone = JSON.parse(JSON.stringify(record)) as Record<string, unknown>;
    const chain: { parent: Record<string, unknown>; leaf: string }[] = [];
    let cur: Record<string, unknown> = clone;
    for (const seg of this.segments) {
      chain.push({ parent: cur, leaf: seg });
      const next = cur[seg];
      if (next === null || typeof next !== 'object' || Array.isArray(next)) break;
      cur = next as Record<string, unknown>;
    }
    const last = chain[chain.length - 1]!;
    delete last.parent[last.leaf];
    for (let i = chain.length - 2; i >= 0; i--) {
      const node = chain[i]!.parent[chain[i]!.leaf];
      if (node === null || typeof node !== 'object' || Array.isArray(node)) break;
      if (Object.keys(node).length > 0) break;
      delete chain[i]!.parent[chain[i]!.leaf];
    }
    return clone;
  }

  private parseOrThrow(text: string, file: string): Record<string, unknown> {
    try {
      const parsed: unknown = JSON.parse(text);
      if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
        throw new Error('not an object');
      }
      return parsed as Record<string, unknown>;
    } catch (err) {
      throw new MalformedConfigError(file, err);
    }
  }
}

// —— 各 agent 共用的归一化器 ——

/** claude-code 风格：{ command,args,env } 或 { type:'http'|'sse', url, headers } */
export function normalizeClaudeStyle(raw: unknown): RawServerEntry | null {
  if (raw === null || typeof raw !== 'object') return null;
  const r = raw as Record<string, unknown>;
  const type = typeof r['type'] === 'string' ? (r['type'] as string) : undefined;
  if (type === 'http' || type === 'sse') {
    if (typeof r['url'] !== 'string') return null;
    return {
      transport: type,
      url: r['url'],
      headers: toStringRecord(r['headers']),
    };
  }
  if (typeof r['url'] === 'string' && r['command'] === undefined) {
    return { transport: 'http', url: r['url'], headers: toStringRecord(r['headers']) };
  }
  if (typeof r['command'] === 'string') {
    return {
      transport: 'stdio',
      command: r['command'],
      args: toStringArray(r['args']),
      env: toStringRecord(r['env']),
      cwd: typeof r['cwd'] === 'string' ? r['cwd'] : undefined,
    };
  }
  return null;
}

export function denormalizeClaudeStyle(entry: RawServerEntry): Record<string, unknown> {
  if (entry.transport === 'stdio') {
    return clean({
      command: entry.command,
      args: entry.args,
      env: entry.env,
      cwd: entry.cwd,
    });
  }
  return clean({
    type: entry.transport === 'sse' ? 'sse' : 'http',
    url: entry.url,
    headers: entry.headers,
  });
}

/** cursor / trae 风格：无 type 字段的 { command,args,env } 或 { url, headers } */
export function normalizeUrlOrCommand(raw: unknown): RawServerEntry | null {
  if (raw === null || typeof raw !== 'object') return null;
  const r = raw as Record<string, unknown>;
  const type = typeof r['type'] === 'string' ? (r['type'] as string) : undefined;
  if (typeof r['url'] === 'string') {
    return {
      transport: type === 'sse' ? 'sse' : 'http',
      url: r['url'],
      headers: toStringRecord(r['headers']),
    };
  }
  if (typeof r['command'] === 'string') {
    return {
      transport: 'stdio',
      command: r['command'],
      args: toStringArray(r['args']),
      env: toStringRecord(r['env']),
    };
  }
  return null;
}

export function denormalizeUrlOrCommand(entry: RawServerEntry): Record<string, unknown> {
  if (entry.transport === 'stdio') {
    return clean({ command: entry.command, args: entry.args, env: entry.env });
  }
  return clean({ url: entry.url, headers: entry.headers });
}

/** opencode 风格：{ type:'local', command:[...], environment } 或 { type:'remote', url } */
export function normalizeOpencode(raw: unknown): RawServerEntry | null {
  if (raw === null || typeof raw !== 'object') return null;
  const r = raw as Record<string, unknown>;
  if (r['type'] === 'remote' && typeof r['url'] === 'string') {
    return { transport: 'http', url: r['url'], headers: toStringRecord(r['headers']) };
  }
  if (r['type'] === 'local' && Array.isArray(r['command'])) {
    const cmd = r['command'].map(String);
    if (cmd.length === 0) return null;
    return {
      transport: 'stdio',
      command: cmd[0],
      args: cmd.slice(1),
      env: toStringRecord(r['environment']),
    };
  }
  return null;
}

export function denormalizeOpencode(entry: RawServerEntry): Record<string, unknown> {
  if (entry.transport === 'stdio') {
    return clean({
      type: 'local',
      command: [entry.command, ...(entry.args ?? [])],
      environment: entry.env,
    });
  }
  return clean({ type: 'remote', url: entry.url, headers: entry.headers });
}

/** Gemini CLI 风格：{ command,args,env } / { httpUrl }（streamable http）/ { url }（sse） */
export function normalizeGeminiStyle(raw: unknown): RawServerEntry | null {
  if (raw === null || typeof raw !== 'object') return null;
  const r = raw as Record<string, unknown>;
  if (typeof r['httpUrl'] === 'string') {
    return { transport: 'http', url: r['httpUrl'], headers: toStringRecord(r['headers']) };
  }
  if (typeof r['url'] === 'string') {
    return { transport: 'sse', url: r['url'], headers: toStringRecord(r['headers']) };
  }
  if (typeof r['command'] === 'string') {
    return {
      transport: 'stdio',
      command: r['command'],
      args: toStringArray(r['args']),
      env: toStringRecord(r['env']),
      cwd: typeof r['cwd'] === 'string' ? r['cwd'] : undefined,
    };
  }
  return null;
}

export function denormalizeGeminiStyle(entry: RawServerEntry): Record<string, unknown> {
  if (entry.transport === 'stdio') {
    return clean({ command: entry.command, args: entry.args, env: entry.env, cwd: entry.cwd });
  }
  if (entry.transport === 'sse') {
    return clean({ url: entry.url, headers: entry.headers });
  }
  return clean({ httpUrl: entry.url, headers: entry.headers });
}

/** WorkBuddy 风格：{ type:'streamableHttp'|'sse', url, timeout?, disabled? } 或 { command,args,env } */
export function normalizeWorkbuddy(raw: unknown): RawServerEntry | null {
  if (raw === null || typeof raw !== 'object') return null;
  const r = raw as Record<string, unknown>;
  const type = typeof r['type'] === 'string' ? (r['type'] as string) : undefined;
  if (typeof r['url'] === 'string') {
    return {
      transport: type === 'sse' ? 'sse' : 'http',
      url: r['url'],
      headers: toStringRecord(r['headers']),
    };
  }
  if (typeof r['command'] === 'string') {
    return {
      transport: 'stdio',
      command: r['command'],
      args: toStringArray(r['args']),
      env: toStringRecord(r['env']),
    };
  }
  return null;
}

export function denormalizeWorkbuddy(entry: RawServerEntry): Record<string, unknown> {
  if (entry.transport === 'stdio') {
    return clean({ command: entry.command, args: entry.args, env: entry.env });
  }
  return clean({
    type: entry.transport === 'sse' ? 'sse' : 'streamableHttp',
    url: entry.url,
    headers: entry.headers,
  });
}

function toStringArray(v: unknown): string[] | undefined {
  return Array.isArray(v) ? v.map(String) : undefined;
}

function toStringRecord(v: unknown): Record<string, string> | undefined {
  if (v === null || typeof v !== 'object') return undefined;
  const out: Record<string, string> = {};
  for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
    if (typeof val === 'string') out[k] = val;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function clean(obj: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(obj)) {
    if (v !== undefined) out[k] = v;
  }
  return out;
}
