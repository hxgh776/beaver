import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { AGENTS } from '../src/agents.js';
import { CodexTomlAdapter, stripManagedSections } from '../src/adapters/codex-toml.js';
import {
  JsonAgentAdapter,
  denormalizeClaudeStyle,
  denormalizeOpencode,
  denormalizeUrlOrCommand,
  normalizeClaudeStyle,
  normalizeOpencode,
  normalizeUrlOrCommand,
} from '../src/adapters/json-adapter.js';
import { MalformedConfigError } from '../src/errors.js';
import { readTextIfExists } from '../src/fsutil.js';
import { stableHash } from '../src/fsutil.js';
import type { RawServerEntry } from '../src/adapters/types.js';

const fixturesRoot = path.join(path.dirname(fileURLToPath(import.meta.url)), 'fixtures');

let sandbox: string;

beforeEach(async () => {
  sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-ad-'));
});

afterEach(async () => {
  await fs.rm(sandbox, { recursive: true, force: true });
});

async function copyFixture(rel: string, destName: string): Promise<string> {
  const dest = path.join(sandbox, destName);
  await fs.copyFile(path.join(fixturesRoot, rel), dest);
  return dest;
}

const FS_ENTRY: RawServerEntry = {
  transport: 'stdio',
  command: 'npx',
  args: ['-y', '@modelcontextprotocol/server-filesystem', 'D:\\proj'],
  env: { FS_ROOT: 'D:\\proj' },
};

function claudeAdapter(file: string): JsonAgentAdapter {
  return new JsonAgentAdapter({
    type: 'claude-code',
    displayName: 'Claude Code',
    transports: ['stdio', 'http'],
    paths: [file],
    containerKey: 'mcpServers',
    normalize: normalizeClaudeStyle,
    denormalize: denormalizeClaudeStyle,
  });
}

describe('claude-code JSON adapter (U-AD-01..05)', () => {
  it('U-AD-01 读取：抽取 mcpServers 并归一化 stdio/http', async () => {
    const file = await copyFixture(path.join('claude-code', 'claude.json'), 'claude.json');
    const servers = await claudeAdapter(file).read(file);
    expect(servers.get('user-server')).toMatchObject({ transport: 'stdio', command: 'uvx' });
    expect(servers.get('remote-api')).toMatchObject({ transport: 'http', url: 'https://api.example.com/mcp' });
  });

  it('U-AD-02 写入：只动 mcpServers，无关顶层键逐键深等', async () => {
    const file = await copyFixture(path.join('claude-code', 'claude.json'), 'claude.json');
    const before = JSON.parse(await fs.readFile(file, 'utf8'));
    const result = await claudeAdapter(file).apply(
      file,
      [{ key: 'filesystem', entry: FS_ENTRY }],
      { previous: [] },
    );
    const after = JSON.parse(await fs.readFile(file, 'utf8'));
    expect(after.theme).toBe(before.theme);
    expect(after.numStartups).toBe(before.numStartups);
    expect(after.projects).toEqual(before.projects);
    expect(after.mcpServers['user-server']).toEqual(before.mcpServers['user-server']);
    expect(after.mcpServers['filesystem']).toMatchObject({ command: 'npx' });
    expect(result.keyStates).toEqual([{ key: 'filesystem', hash: expect.any(String) }]);
  });

  it('U-AD-03 删除：仅移除受管键，其余保留', async () => {
    const file = await copyFixture(path.join('claude-code', 'claude.json'), 'claude.json');
    const adapter = claudeAdapter(file);
    await adapter.apply(file, [{ key: 'filesystem', entry: FS_ENTRY }], { previous: [] });
    const keyStates = (await adapter.read(file)) && [];
    void keyStates;
    const hash = stableHash(JSON.parse(await fs.readFile(file, 'utf8')).mcpServers['filesystem']);
    await adapter.apply(file, [{ key: 'filesystem', entry: null }], {
      previous: [{ key: 'filesystem', hash }],
    });
    const after = JSON.parse(await fs.readFile(file, 'utf8'));
    expect(after.mcpServers['filesystem']).toBeUndefined();
    expect(after.mcpServers['user-server']).toBeDefined();
    expect(after.theme).toBe('dark');
  });

  it('U-AD-04 坏 JSON：抛 MalformedConfigError 且原文件未动', async () => {
    const file = path.join(sandbox, 'bad.json');
    await fs.writeFile(file, '{ not json', 'utf8');
    await expect(
      claudeAdapter(file).apply(file, [{ key: 'x', entry: FS_ENTRY }], { previous: [] }),
    ).rejects.toThrow(MalformedConfigError);
    expect(await fs.readFile(file, 'utf8')).toBe('{ not json');
  });

  it('U-AD-05 备份回调：真实变更触发一次，无变更/dryRun 不触发', async () => {
    const file = path.join(sandbox, 'claude2.json');
    await copyFixture(path.join('claude-code', 'claude.json'), 'claude2.json');
    const adapter = claudeAdapter(file);
    let backups = 0;
    await adapter.apply(file, [{ key: 'filesystem', entry: FS_ENTRY }], {
      previous: [],
      backup: async () => {
        backups += 1;
      },
    });
    expect(backups).toBe(1);
    await adapter.apply(file, [{ key: 'filesystem', entry: FS_ENTRY }], { previous: [], backup: async () => void (backups += 1) });
    expect(backups).toBe(1); // 幂等重放无变更
    let dryRunBackups = 0;
    await adapter.apply(
      file,
      [{ key: 'other', entry: { transport: 'stdio', command: 'x' } }],
      { previous: [], dryRun: true, backup: async () => void (dryRunBackups += 1) },
    );
    expect(dryRunBackups).toBe(0);
  });
});

describe('conflict detection (U-CF)', () => {
  it('上次写入的片段被手改 → 冲突默认跳过，override 才写入', async () => {
    const file = path.join(sandbox, 'claude3.json');
    await copyFixture(path.join('claude-code', 'claude.json'), 'claude3.json');
    const adapter = claudeAdapter(file);
    const first = await adapter.apply(file, [{ key: 'filesystem', entry: FS_ENTRY }], { previous: [] });
    const ks = first.keyStates.find((k) => k.key === 'filesystem')!;

    // 用户手改
    const doc = JSON.parse(await fs.readFile(file, 'utf8'));
    doc.mcpServers['filesystem'].args = ['-y', 'hacked'];
    await fs.writeFile(file, JSON.stringify(doc, null, 2), 'utf8');

    const changed: RawServerEntry = { ...FS_ENTRY, args: ['-y', '@modelcontextprotocol/server-filesystem', 'D:\\new'] };
    const skip = await adapter.apply(file, [{ key: 'filesystem', entry: changed }], {
      previous: [ks],
    });
    expect(skip.conflicts).toHaveLength(1);
    expect(skip.conflicts[0]!.resolution).toBe('skip');
    const untouched = JSON.parse(await fs.readFile(file, 'utf8'));
    expect(untouched.mcpServers['filesystem'].args).toEqual(['-y', 'hacked']);

    const override = await adapter.apply(file, [{ key: 'filesystem', entry: changed }], {
      previous: [ks],
      resolutions: { filesystem: 'override' },
    });
    expect(override.conflicts[0]!.resolution).toBe('override');
    const overridden = JSON.parse(await fs.readFile(file, 'utf8'));
    expect(overridden.mcpServers['filesystem'].args).toEqual(['-y', '@modelcontextprotocol/server-filesystem', 'D:\\new']);
  });

  it('删除无 previous 记录的键 → 视为冲突，绝不静默删除', async () => {
    const file = await copyFixture(path.join('claude-code', 'claude.json'), 'claude4.json');
    const result = await claudeAdapter(file).apply(
      file,
      [{ key: 'user-server', entry: null }],
      { previous: [] },
    );
    expect(result.conflicts.map((c) => c.key)).toContain('user-server');
    const after = JSON.parse(await fs.readFile(file, 'utf8'));
    expect(after.mcpServers['user-server']).toBeDefined();
  });
});

describe('codex TOML adapter (U-AD-06..08)', () => {
  function codexAdapter(file: string): CodexTomlAdapter {
    return new CodexTomlAdapter([file]);
  }

  it('U-AD-06 读取：解析 [mcp_servers.*]', async () => {
    const file = await copyFixture(path.join('codex', 'config.toml'), 'config.toml');
    const servers = await codexAdapter(file).read(file);
    expect(servers.get('user_own')).toMatchObject({ transport: 'stdio', command: 'user-server' });
  });

  it('U-AD-07 写入：非托管区注释与顺序字节级保留', async () => {
    const file = await copyFixture(path.join('codex', 'config.toml'), 'config.toml');
    const before = await fs.readFile(file, 'utf8');
    const adapter = codexAdapter(file);
    const result = await adapter.apply(
      file,
      [{ key: 'filesystem', entry: { ...FS_ENTRY, env: { API_KEY: 'secret-1' } } }],
      { previous: [] },
    );
    expect(result.changes).toEqual([{ key: 'filesystem', action: 'upsert' }]);

    const after = await fs.readFile(file, 'utf8');
    // 字节级：非托管区完全一致
    const managed = new Set(['filesystem', 'user_own']);
    expect(stripManagedSections(after, managed)).toBe(stripManagedSections(before, managed));
    expect(after).toContain('# 用户自定义注释，必须原样保留');
    expect(after).toContain('# 托管区外的注释');
    expect(after).toContain('[profiles.fast]');

    // 语义：新段可解析、env 保真
    const servers = await adapter.read(file);
    expect(servers.get('filesystem')).toMatchObject({ command: 'npx', env: { API_KEY: 'secret-1' } });
  });

  it('U-AD-07b 幂等重放：同内容再次 apply 产生 no-change', async () => {
    const file = await copyFixture(path.join('codex', 'config.toml'), 'config.toml');
    const adapter = codexAdapter(file);
    const write = [{ key: 'filesystem', entry: FS_ENTRY }];
    const first = await adapter.apply(file, write, { previous: [] });
    const second = await adapter.apply(file, write, {
      previous: first.keyStates,
    });
    expect(second.changes).toEqual([{ key: 'filesystem', action: 'none' }]);
  });

  it('U-AD-08 手改检测 + 删除保护', async () => {
    const file = await copyFixture(path.join('codex', 'config.toml'), 'config.toml');
    const adapter = codexAdapter(file);
    const first = await adapter.apply(file, [{ key: 'filesystem', entry: FS_ENTRY }], { previous: [] });
    const ks = first.keyStates.find((k) => k.key === 'filesystem')!;

    // 用户手改托管段
    const doc = await fs.readFile(file, 'utf8');
    await fs.writeFile(file, doc.replace('"npx"', '"npx-hacked"'), 'utf8');

    const second = await adapter.apply(
      file,
      [{ key: 'filesystem', entry: { ...FS_ENTRY, command: 'bunx' } }],
      { previous: [ks] },
    );
    expect(second.conflicts).toHaveLength(1);
    expect(await fs.readFile(file, 'utf8')).toContain('"npx-hacked"');

    // 删除非本工具所写的键 → 冲突
    const rm = await adapter.apply(file, [{ key: 'user_own', entry: null }], { previous: [] });
    expect(rm.conflicts.map((c) => c.key)).toContain('user_own');
    expect((await adapter.read(file)).get('user_own')).toBeDefined();
  });
});

describe('normalize/denormalize round-trips (U-AD-09..11)', () => {
  it('U-AD-09 cursor 风格 stdio/http 往返', () => {
    const stdio = { transport: 'stdio' as const, command: 'node', args: ['s.js'], env: { A: '1' } };
    const http = { transport: 'http' as const, url: 'https://x/mcp', headers: { H: 'v' } };
    expect(normalizeUrlOrCommand(denormalizeUrlOrCommand(stdio))).toEqual(stdio);
    expect(normalizeUrlOrCommand(denormalizeUrlOrCommand(http))).toEqual(http);
  });

  it('U-AD-11 opencode local/remote 往返（command 数组/environment）', () => {
    const local = { transport: 'stdio' as const, command: 'npx', args: ['-y', 'srv'], env: { KEY: 'v' } };
    const remote = { transport: 'http' as const, url: 'https://y/mcp' };
    expect(normalizeOpencode(denormalizeOpencode(local))).toEqual(local);
    expect(normalizeOpencode(denormalizeOpencode(remote))).toEqual(remote);
    // 与 fixture 中真实形态一致
    expect(normalizeOpencode({ type: 'local', command: ['npx', '-y', 'server-x'], environment: { KEY: 'v' } })).toEqual({
      transport: 'stdio',
      command: 'npx',
      args: ['-y', 'server-x'],
      env: { KEY: 'v' },
    });
  });

  it('注册表：5 个 agent 均有适配器与候选路径', () => {
    for (const def of Object.values(AGENTS)) {
      expect(def.adapter.candidatePaths().length).toBeGreaterThan(0);
      expect(def.displayName.length).toBeGreaterThan(0);
    }
    expect(AGENTS['codex']!.adapter.transports).toEqual(['stdio']);
  });
});

describe('JSON adapter dryRun', () => {
  it('dryRun 返回计划与预期 hash，不落盘', async () => {
    const file = await copyFixture(path.join('claude-code', 'claude.json'), 'claude5.json');
    const adapter = claudeAdapter(file);
    const before = await readTextIfExists(file);
    const result = await adapter.apply(
      file,
      [{ key: 'filesystem', entry: FS_ENTRY }, { key: 'user-server', entry: null }],
      { previous: [], dryRun: true },
    );
    expect(result.changes).toContainEqual({ key: 'filesystem', action: 'upsert' });
    // user-server 非本工具所写（无 previous 记录）→ 按设计判为冲突，计划中为 no-op
    expect(result.changes).toContainEqual({ key: 'user-server', action: 'none' });
    expect(result.conflicts.map((c) => c.key)).toContain('user-server');
    expect(await readTextIfExists(file)).toBe(before);
  });
});
