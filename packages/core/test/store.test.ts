import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { backupFile } from '../src/backup.js';
import { atomicWriteFile, stableHash, stableStringify } from '../src/fsutil.js';
import { maybeMigrateHome, resolveHomeDir, Store } from '../src/store.js';

let sandbox: string;

beforeEach(async () => {
  sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'mcpmgr-store-'));
});

afterEach(async () => {
  await fs.rm(sandbox, { recursive: true, force: true });
});

describe('atomicWriteFile (U-ST-01)', () => {
  it('自动创建父目录并写入', async () => {
    const file = path.join(sandbox, 'a/b/c.json');
    await atomicWriteFile(file, '{"x":1}');
    expect(await fs.readFile(file, 'utf8')).toBe('{"x":1}');
  });

  it('覆盖已有文件且不残留临时文件', async () => {
    const file = path.join(sandbox, 'f.json');
    await atomicWriteFile(file, 'v1');
    await atomicWriteFile(file, 'v2');
    expect(await fs.readFile(file, 'utf8')).toBe('v2');
    const leftovers = (await fs.readdir(sandbox)).filter((n) => n.endsWith('.tmp'));
    expect(leftovers).toEqual([]);
  });
});

describe('Store persistence (U-ST-02)', () => {
  it('save → load 往返一致', async () => {
    const store = new Store(sandbox);
    const config = await store.load();
    config.servers.push({
      id: 'filesystem',
      name: 'Filesystem',
      transport: 'stdio',
      command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-filesystem', 'D:\\tmp'],
      gatewayMode: false,
      enabled: true,
      createdAt: '2026-01-01T00:00:00.000Z',
      updatedAt: '2026-01-01T00:00:00.000Z',
    });
    await store.save(config);

    const reloaded = await new Store(sandbox).load();
    expect(reloaded.servers).toEqual(config.servers);
  });

  it('缺省配置：token 已生成、端口 6280', async () => {
    const config = await new Store(sandbox).load();
    expect(config.settings.port).toBe(6280);
    expect(config.settings.token).toMatch(/^[0-9a-f]{48}$/);
    expect(config.version).toBe(1);
  });
});

describe('Store corruption handling (U-ST-03)', () => {
  it('config.json 损坏 → 移为 .corrupt-* 并重建默认配置', async () => {
    const store = new Store(sandbox);
    await store.load(); // 生成合法文件
    const withServer = await store.load();
    withServer.servers.push({
      id: 'x',
      name: 'X',
      transport: 'stdio',
      command: 'foo',
      gatewayMode: false,
      enabled: true,
      createdAt: '2026-01-01T00:00:00.000Z',
      updatedAt: '2026-01-01T00:00:00.000Z',
    });
    await store.save(withServer);

    await fs.writeFile(store.configPath, '{ not valid json !!', 'utf8');
    const recovered = await store.load();
    expect(recovered.servers).toEqual([]);
    const dirEntries = await fs.readdir(sandbox);
    expect(dirEntries.some((n) => n.startsWith('config.json.corrupt-'))).toBe(true);
  });

  it('校验失败（结构非法）同样触发保护', async () => {
    const store = new Store(sandbox);
    await fs.writeFile(store.configPath, '{"version":2,"servers":"nope"}', 'utf8');
    const config = await store.load();
    expect(config.version).toBe(1);
  });
});

describe('resolveHomeDir (U-ST-07)', () => {
  const original = process.env['AGENT_CONTROL_HOME'];

  it('显式参数优先于环境变量，环境变量优先于 home', () => {
    try {
      process.env['AGENT_CONTROL_HOME'] = 'D:\\sandbox-home';
      expect(resolveHomeDir('D:\\explicit')).toBe('D:\\explicit');
      expect(resolveHomeDir()).toBe('D:\\sandbox-home');
      delete process.env['AGENT_CONTROL_HOME'];
      expect(resolveHomeDir()).toBe(path.join(os.homedir(), '.beaver'));
    } finally {
      if (original === undefined) delete process.env['AGENT_CONTROL_HOME'];
      else process.env['AGENT_CONTROL_HOME'] = original;
    }
  });

  it('旧环境变量名 MCP_MANAGER_HOME 仍被兼容（升级过渡期）', () => {
    try {
      delete process.env['AGENT_CONTROL_HOME'];
      process.env['MCP_MANAGER_HOME'] = 'D:/legacy-home';
      expect(resolveHomeDir()).toBe('D:/legacy-home');
    } finally {
      delete process.env['MCP_MANAGER_HOME'];
    }
  });
});

describe('backupFile (U-ST-05)', () => {
  it('文件不存在 → null', async () => {
    expect(await backupFile(path.join(sandbox, 'nope.json'), 'claude-code', path.join(sandbox, 'backups'))).toBeNull();
  });

  it('备份内容一致，超过 keep 份时滚动删除最旧', async () => {
    const file = path.join(sandbox, 'claude.json');
    const backupsRoot = path.join(sandbox, 'backups');
    await fs.writeFile(file, 'v1', 'utf8');
    for (let i = 0; i < 12; i++) {
      await fs.writeFile(file, `v${i}`, 'utf8');
      // 固定宽度时间戳含毫秒，同毫秒内连写可能同名覆盖——用 keep=10 仍应收敛
      await new Promise((r) => setTimeout(r, 3));
      await backupFile(file, 'claude-code', backupsRoot, 10);
    }
    const files = await fs.readdir(path.join(backupsRoot, 'claude-code'));
    expect(files.length).toBe(10);
    const newest = await fs.readFile(path.join(backupsRoot, 'claude-code', files.at(-1)!), 'utf8');
    expect(newest).toBe('v11');
  });
});

describe('stableHash (U-ST-06)', () => {
  it('键序无关、内容敏感', () => {
    expect(stableHash({ a: 1, b: { c: 2, d: 3 } })).toBe(stableHash({ b: { d: 3, c: 2 }, a: 1 }));
    expect(stableHash({ a: 1 })).not.toBe(stableHash({ a: 2 }));
    expect(stableStringify({ x: undefined, y: 1 })).toBe('{"y":1}');
  });
});

describe('maybeMigrateHome（数据目录迁移）', () => {
  it('新目录不存在 + 旧目录存在 → 整体改名；旧目录占用 → 沿用旧目录', async () => {
    const base = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-migrate-'));
    try {
      const legacy = path.join(base, 'legacy-home');
      const next = path.join(base, 'new-home');
      await fs.mkdir(legacy);
      await fs.writeFile(path.join(legacy, 'config.json'), '{"marker":true}', 'utf8');
      expect(maybeMigrateHome(legacy, next)).toBe(next);
      await expect(fs.access(path.join(legacy))).rejects.toThrow(); // 旧目录已让位
      expect(JSON.parse(await fs.readFile(path.join(next, 'config.json'), 'utf8'))).toEqual({ marker: true });
      // 再跑一次幂等
      expect(maybeMigrateHome(legacy, next)).toBe(next);
      // 新目录已存在：不动
      const occupied = path.join(base, 'occupied');
      await fs.mkdir(occupied);
      await fs.writeFile(path.join(occupied, 'config.json'), '{"v":1}', 'utf8');
      expect(maybeMigrateHome(legacy, occupied)).toBe(occupied);
      // 新目录存在但为空（被空跑创建）→ 让位迁移
      const legacy2 = path.join(base, 'legacy-2');
      const emptyNew = path.join(base, 'empty-new');
      await fs.mkdir(legacy2);
      await fs.writeFile(path.join(legacy2, 'config.json'), '{"marker":2}', 'utf8');
      await fs.mkdir(emptyNew);
      expect(maybeMigrateHome(legacy2, emptyNew)).toBe(emptyNew);
      expect(JSON.parse(await fs.readFile(path.join(emptyNew, 'config.json'), 'utf8'))).toEqual({ marker: 2 });
    } finally {
      await fs.rm(base, { recursive: true, force: true });
    }
  });
});
