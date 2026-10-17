import { promises as fs } from 'node:fs';
import { createHash } from 'node:crypto';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { makeAgents } from '../src/agents.js';
import { Store } from '../src/store.js';
import { SyncEngine } from '../src/sync.js';
import { setBinding, upsertServer } from '../src/registry.js';
import {
  PublicationJournal,
  firstWriteBackup,
  recoverPendingPublications,
} from '../src/write-engine.js';
import { CodexTomlAdapter } from '../src/adapters/codex-toml.js';
import { JsonAgentAdapter, denormalizeClaudeStyle, normalizeClaudeStyle } from '../src/adapters/json-adapter.js';
import { MalformedConfigError } from '../src/errors.js';
import type { ManagerConfig } from '../src/types.js';

let sandbox: string;
let store: Store;

beforeEach(async () => {
  sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-we-'));
  store = new Store(sandbox);
});

afterEach(async () => {
  await fs.rm(sandbox, { recursive: true, force: true });
});

const sha = (s: string) => createHash('sha256').update(s).digest('hex');

async function writeAgentFile(relSegments: string[], content: string): Promise<string> {
  const file = path.join(sandbox, ...relSegments);
  await fs.mkdir(path.dirname(file), { recursive: true });
  await fs.writeFile(file, content, 'utf8');
  return file;
}

describe('S0 首写备份 (U-WE-01)', () => {
  it('首次写入前备份原始字节，且每文件只做一次', async () => {
    const file = await writeAgentFile(['.cursor', 'mcp.json'], '{"mcpServers":{"a":{"command":"orig"}}}');
    const bak = await firstWriteBackup(file, store.backupsDir);
    expect(bak).not.toBeNull();
    expect(await fs.readFile(bak!, 'utf8')).toBe('{"mcpServers":{"a":{"command":"orig"}}}');
    const marker = await fs.readdir(path.join(store.backupsDir, 'first-write'));
    expect(marker.filter((m) => m.endsWith('.source'))).toHaveLength(1);

    // 内容变化后再备份 → 返回 null（已做过），备份保持首写时的字节
    await fs.writeFile(file, '{"changed":true}', 'utf8');
    expect(await firstWriteBackup(file, store.backupsDir)).toBeNull();
    const files = await fs.readdir(path.join(store.backupsDir, 'first-write'));
    const bakName = files.find((f) => f.endsWith('.bak'))!;
    expect(await fs.readFile(path.join(store.backupsDir, 'first-write', bakName), 'utf8')).toBe(
      '{"mcpServers":{"a":{"command":"orig"}}}',
    );
  });

  it('文件不存在时首写备份为 no-op；不同文件各自有独立备份', async () => {
    const ghost = path.join(sandbox, '.cursor', 'nope.json');
    expect(await firstWriteBackup(ghost, store.backupsDir)).toBeNull();
    const a = await writeAgentFile(['a.json'], '{"x":1}');
    const b = await writeAgentFile(['b.json'], '{"y":2}');
    expect(await firstWriteBackup(a, store.backupsDir)).not.toBeNull();
    expect(await firstWriteBackup(b, store.backupsDir)).not.toBeNull();
    const files = await fs.readdir(path.join(store.backupsDir, 'first-write'));
    expect(files.filter((f) => f.endsWith('.bak'))).toHaveLength(2);
  });

  it('经 SyncEngine 完整链路：写一次即产生首写备份，发布日志正常清账', async () => {
    const engine = new SyncEngine({ backupsDir: store.backupsDir, homeDir: store.homeDir, agents: makeAgents(sandbox) });
    const config: ManagerConfig = await store.load();
    upsertServer(config, { id: 'fs', name: 'fs', transport: 'stdio', command: 'npx', args: ['-y', 'srv-fs'] });
    setBinding(config, 'fs', 'cursor');
    const file = await writeAgentFile(['.cursor', 'mcp.json'], '{"mcpServers":{}}');
    await engine.sync(config);

    const fwDir = path.join(store.backupsDir, 'first-write');
    const baks = (await fs.readdir(fwDir)).filter((f) => f.endsWith('.bak'));
    expect(baks).toHaveLength(1);
    expect(await fs.readFile(path.join(fwDir, baks[0]!), 'utf8')).toBe('{"mcpServers":{}}');
    // 成功发布 → 发布日志不存在
    await expect(fs.access(path.join(store.homeDir, 'pending-publication.json'))).rejects.toThrow();
    expect((await fs.readFile(file, 'utf8')).includes('"fs"')).toBe(true);
  });
});

describe('S0 发布日志三态前滚 (U-WE-02)', () => {
  it('未发布/已发布/发布后被改 三态判定正确，恢复后日志删除', async () => {
    const pre = '{"mcpServers":{}}';
    const planned = '{"mcpServers":{"a":{"command":"new"}}}';
    const fileA = await writeAgentFile(['a.json'], pre);
    const fileB = await writeAgentFile(['b.json'], pre);
    const fileC = await writeAgentFile(['c.json'], pre);

    const journal = new PublicationJournal(store.homeDir);
    await journal.record(fileA, pre, planned); // A：模拟 rename 前崩溃 → 未发布
    await journal.record(fileB, pre, planned);
    await fs.writeFile(fileB, planned, 'utf8'); // B：rename 已发生 → 已发布
    await journal.record(fileC, pre, planned);
    await fs.writeFile(fileC, '{"user":"edited"}', 'utf8'); // C：发布后被用户改
    await journal.complete(fileB); // 模拟 B 清账后崩溃（日志仍在）

    const report = await recoverPendingPublications(store.homeDir);
    expect(report).not.toBeNull();
    const byPath = new Map(report!.map((r) => [r.path, r.state]));
    expect(byPath.get(fileA)).toBe('unpublished');
    expect(byPath.get(fileB)).toBe('published');
    expect(byPath.get(fileC)).toBe('modified-after-publish');
    // 恢复只上报不回写；日志文件删除
    await expect(fs.access(store.homeDir + '/pending-publication.json')).rejects.toThrow();
    expect(await fs.readFile(fileA, 'utf8')).toBe(pre);
  });

  it('无日志 → null；损坏日志 → 挪到 .corrupt-* 留证', async () => {
    expect(await recoverPendingPublications(store.homeDir)).toBeNull();
    const journalFile = path.join(store.homeDir, 'pending-publication.json');
    await fs.writeFile(journalFile, '{not json', 'utf8');
    expect(await recoverPendingPublications(store.homeDir)).toBeNull();
    const entries = await fs.readdir(store.homeDir);
    expect(entries.some((e) => e.startsWith('pending-publication.json.corrupt-'))).toBe(true);
  });

  it('record→complete 全清后删除日志；同文件重试覆盖条目', async () => {
    const journalFile = path.join(store.homeDir, 'pending-publication.json');
    const journal = new PublicationJournal(store.homeDir);
    const file = await writeAgentFile(['x.json'], 'v1');
    await journal.record(file, 'v0', 'v1');
    await journal.record(file, 'v1', 'v2'); // 重试覆盖
    const state = JSON.parse(await fs.readFile(journalFile, 'utf8'));
    expect(state.files).toHaveLength(1);
    expect(state.files[0].plannedHash).toBe(sha('v2'));
    await fs.writeFile(file, 'v2', 'utf8');
    await journal.complete(file);
    await expect(fs.access(journalFile)).rejects.toThrow();
  });
});

describe('S0 解析失败绝不从空文档写 (U-WE-03)', () => {
  it('JSON 适配器：损坏文件 apply 直接中止，内容一个字节不动', async () => {
    const file = await writeAgentFile(['.cursor', 'mcp.json'], '{broken json!!');
    const adapter = new JsonAgentAdapter({
      type: 'cursor',
      displayName: 'Cursor',
      transports: ['stdio', 'http', 'sse'],
      paths: [file],
      containerKey: 'mcpServers',
      normalize: normalizeClaudeStyle,
      denormalize: (e) => ({ command: e.command }),
    });
    await expect(adapter.apply(file, [{ key: 'a', entry: { transport: 'stdio', command: 'npx' } }], { previous: [] }))
      .rejects.toBeInstanceOf(MalformedConfigError);
    expect(await fs.readFile(file, 'utf8')).toBe('{broken json!!');
  });

  it('TOML 适配器：损坏文件 apply 直接中止', async () => {
    const file = await writeAgentFile(['.codex', 'config.toml'], '[mcp_servers.a\nbroken===');
    const adapter = new CodexTomlAdapter([file]);
    await expect(adapter.apply(file, [{ key: 'b', entry: { transport: 'stdio', command: 'npx' } }], { previous: [] }))
      .rejects.toBeInstanceOf(MalformedConfigError);
    expect(await fs.readFile(file, 'utf8')).toBe('[mcp_servers.a\nbroken===');
  });
});

describe('嵌套容器写后校验 (C6)', () => {
  const makeZcode = (file: string) =>
    new JsonAgentAdapter({
      type: 'zcode',
      displayName: 'ZCode',
      transports: ['stdio', 'http', 'sse'],
      paths: [file],
      containerKey: 'mcp.servers',
      normalize: normalizeClaudeStyle,
      denormalize: denormalizeClaudeStyle,
    });

  it('首次写入新建中间层（无 mcp 键）→ 校验通过不回滚', async () => {
    // 回归：写入新建的空中间层壳在校验时须剪除，否则首次分发必然误判回滚
    const file = await writeAgentFile(['.zcode', 'cli', 'config.json'], JSON.stringify({ plugins: { a: true } }));
    const result = await makeZcode(file).apply(
      file,
      [{ key: 'beaver', entry: { transport: 'http', url: 'http://127.0.0.1:6280/mcp' } }],
      { previous: [] },
    );
    expect(result.changes).toEqual([{ key: 'beaver', action: 'upsert' }]);
    const after = JSON.parse(await fs.readFile(file, 'utf8'));
    expect(after.mcp.servers.beaver).toEqual({ type: 'http', url: 'http://127.0.0.1:6280/mcp' });
    expect(after.plugins).toEqual({ a: true });
  });

  it('中间层已有兄弟键 → 摘叶后不误剪，校验通过', async () => {
    const file = await writeAgentFile(
      ['.zcode', 'cli', 'config.json'],
      JSON.stringify({ mcp: { other: { keep: 1 } } }),
    );
    await makeZcode(file).apply(
      file,
      [{ key: 'beaver', entry: { transport: 'stdio', command: 'npx' } }],
      { previous: [] },
    );
    const after = JSON.parse(await fs.readFile(file, 'utf8'));
    expect(after.mcp.other).toEqual({ keep: 1 });
    expect(after.mcp.servers.beaver).toEqual({ command: 'npx' });
  });

  it('中间层是非对象 → MALFORMED_CONFIG 且文件一个字节不动', async () => {
    const raw = '{"mcp":"not-an-object"}';
    const file = await writeAgentFile(['.zcode', 'cli', 'config.json'], raw);
    await expect(
      makeZcode(file).apply(file, [{ key: 'beaver', entry: { transport: 'stdio', command: 'npx' } }], { previous: [] }),
    ).rejects.toBeInstanceOf(MalformedConfigError);
    expect(await fs.readFile(file, 'utf8')).toBe(raw);
  });
});
