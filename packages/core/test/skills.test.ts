import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Store } from '../src/store.js';
import { SkillsService, sanitizeInstallName, resolveSkillSourceDir, scanSkillDirs } from '../src/skills/service.js';
import { computeDirHash } from '../src/skills/hash.js';
import { safeExtractPath } from '../src/skills/zip.js';
import { SkillInputError, searchSkillsSh, validateRepoRef, githubArchiveUrl } from '../src/skills/skills-sh.js';
import { SKILL_AGENT_LIST } from '../src/skills/types.js';
import type { ManagerConfig } from '../src/types.js';

let sandbox: string;
let store: Store;
let config: ManagerConfig;
let service: SkillsService;

const fixtureDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '../../../fixtures/skills');
const readFixture = async (name: string): Promise<Buffer> => fs.readFile(path.join(fixtureDir, name));

beforeEach(async () => {
  sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-skills-'));
  store = new Store(sandbox);
  config = await store.load();
  service = new SkillsService({
    store,
    getConfig: () => config,
    saveConfig: async () => {},
    agentRoot: sandbox,
  });
});

afterEach(async () => {
  await fs.rm(sandbox, { recursive: true, force: true });
  vi.unstubAllGlobals();
});

describe('U-SK-01 content_hash', () => {
  it('内容相同、目录顺序不同 → 同哈希；任一文件变化 → 不同哈希；隐藏文件不计入', async () => {
    const d1 = path.join(sandbox, 'd1');
    const d2 = path.join(sandbox, 'd2');
    await fs.mkdir(path.join(d1, 'sub'), { recursive: true });
    await fs.mkdir(path.join(d2, 'sub'), { recursive: true });
    await fs.writeFile(path.join(d1, 'b.txt'), 'B');
    await fs.writeFile(path.join(d1, 'sub', 'a.txt'), 'A');
    await fs.writeFile(path.join(d2, 'sub', 'a.txt'), 'A');
    await fs.writeFile(path.join(d2, 'b.txt'), 'B');
    expect(await computeDirHash(d1)).toBe(await computeDirHash(d2));

    await fs.writeFile(path.join(d2, 'sub', 'a.txt'), 'A2');
    expect(await computeDirHash(d2)).not.toBe(await computeDirHash(d1));

    await fs.writeFile(path.join(d2, '.hidden'), 'ignored');
    expect(await computeDirHash(d2)).not.toBe(await computeDirHash(d1));
  });
});

describe('U-SK-02 输入校验', () => {
  it('sanitizeInstallName：分隔符/穿越/保留名', () => {
    expect(sanitizeInstallName('my skill!')).toBe('my-skill-');
    expect(sanitizeInstallName('../../etc/passwd')).toBe('etc-passwd');
    expect(sanitizeInstallName('')).toBe('skill');
    expect(sanitizeInstallName('..')).toBe('skill');
    expect(sanitizeInstallName('a\\b/c')).toBe('a-b-c');
    expect(sanitizeInstallName('.hidden')).toBe('hidden');
    expect(sanitizeInstallName('demo-skill')).toBe('demo-skill');
  });

  it('validateRepoRef / 归档 URL 出口断言', () => {
    expect(validateRepoRef('anthropics/skills', 'main')).toEqual({ owner: 'anthropics', name: 'skills', branch: 'main' });
    expect(() => validateRepoRef('skills.volces.com/repo', 'main')).toThrow(SkillInputError);
    expect(() => validateRepoRef('a/b', '../../../releases/download/v1/evil')).toThrow(SkillInputError);
    expect(() => githubArchiveUrl('o', 'r', '../evil')).toThrow(SkillInputError);
    expect(githubArchiveUrl('o', 'r', 'main')).toBe('https://github.com/o/r/archive/refs/heads/main.zip');
  });

  it('safeExtractPath：zip-slip 双重防线', () => {
    const root = path.resolve(sandbox, 'dest');
    expect(safeExtractPath(root, 'repo-main/SKILL.md')).not.toBeNull();
    expect(safeExtractPath(root, 'repo-main/sub/../ok.txt')).toBeNull(); // `..` 任何位置整体拒收
    expect(safeExtractPath(root, 'repo-main/../evil.txt')).toBeNull();
    expect(safeExtractPath(root, '../evil.txt')).toBeNull();
    expect(safeExtractPath(root, 'C:/evil.txt')).toBeNull();
    expect(safeExtractPath(root, 'repo-main/', 'repo-main')).toBe(root); // 归档根目录条目（剥根后）→ 目标根本身（供 mkdir）
    expect(safeExtractPath(root, 'repo-main/')).toBe(path.join(root, 'repo-main')); // 未剥根时为普通子目录
  });
});

describe('U-SK-03 安装/投影/卸载（本地 ZIP 全链路）', () => {
  it('单 skill 安装 → SSOT + 关联表全关 → 启用投影 → 停用移除 → 卸载留备份', async () => {
    const row = await service.installFromLocalZip(await readFixture('demo-skill.zip'));
    expect(row.directory).toBe('demo-skill');
    expect(row.name).toBe('demo-skill');
    expect(row.source).toBe('local');
    const ssot = path.join(store.homeDir, 'skills', 'demo-skill');
    expect(await computeDirHash(ssot)).toBe(row.contentHash);
    // 关联表：全部 skill agent 默认关闭
    expect(config.skillTargets.filter((t) => t.skillId === row.id)).toHaveLength(SKILL_AGENT_LIST.length);
    expect(config.skillTargets.every((t) => !t.enabled)).toBe(true);

    await service.toggle(row.id, 'claude-code', true);
    const projected = path.join(sandbox, '.claude', 'skills', 'demo-skill');
    expect((await fs.readFile(path.join(projected, 'SKILL.md'), 'utf8')).includes('Demo')).toBe(true);
    const listed = await service.list();
    const cc = listed[0]!.targets.find((t) => t.agentType === 'claude-code')!;
    expect(cc.projected).toBe(true);
    expect(cc.modified).toBe(false);

    await service.toggle(row.id, 'claude-code', false);
    await expect(fs.access(projected)).rejects.toThrow();

    const result = await service.uninstall(row.id);
    expect(result.preserved).toEqual([]);
    expect(result.backup).not.toBeNull();
    expect(await fs.readdir(path.join(result.backup!, 'skill'))).toContain('SKILL.md');
    expect((await fs.readFile(path.join(result.backup!, 'meta.json'), 'utf8')).includes('demo-skill')).toBe(true);
    expect(config.skills).toHaveLength(0);
    expect(config.skillTargets).toHaveLength(0);
    await expect(fs.access(ssot)).rejects.toThrow();
  });

  it('安装时 enableFor：投影的同时点亮启用位（UI 一致性）', async () => {
    const row = await service.installFromLocalZip(await readFixture('demo-skill.zip'), {
      enableFor: ['claude-code', 'zcode'],
    });
    const targets = config.skillTargets.filter((t) => t.skillId === row.id);
    expect(targets.find((t) => t.agentType === 'claude-code')!.enabled).toBe(true);
    expect(targets.find((t) => t.agentType === 'zcode')!.enabled).toBe(true);
    expect(targets.find((t) => t.agentType === 'cursor')!.enabled).toBe(false);
    const listed = await service.list();
    const byAgent = new Map(listed[0]!.targets.map((t) => [t.agentType, t]));
    expect(byAgent.get('claude-code')).toMatchObject({ enabled: true, projected: true, modified: false });
    expect(byAgent.get('zcode')).toMatchObject({ enabled: true, projected: true, modified: false });
  });

  it('用户改过的目标目录：启用拒绝覆盖，停用原样保留', async () => {
    const row = await service.installFromLocalZip(await readFixture('demo-skill.zip'));
    await service.toggle(row.id, 'claude-code', true); // 先正常投影
    const projected = path.join(sandbox, '.claude', 'skills', 'demo-skill');
    await fs.writeFile(path.join(projected, 'USER-NOTE.md'), 'user edited', 'utf8'); // 用户随后改了目标

    // 停用：不是我们的原样副本 → 保留
    const off = await service.toggle(row.id, 'claude-code', false);
    expect(off.preserved).toBe(projected);
    await expect(fs.access(path.join(projected, 'USER-NOTE.md'))).resolves.toBeUndefined();

    // 再启用：目标存在且内容不同 → 拒绝覆盖
    await expect(service.toggle(row.id, 'claude-code', true)).rejects.toThrow(/拒绝覆盖/);
  });

  it('多 skill 仓库：按目录名指定安装；同来源复用刷新；异来源（repo vs 本地）目录名冲突报错', async () => {
    const beta = await service.installFromLocalZip(await readFixture('multi-skill.zip'), { skill: 'beta' });
    expect(beta.directory).toBe('beta');
    const alpha = await service.installFromLocalZip(await readFixture('multi-skill.zip'), { skill: 'alpha' });
    expect(alpha.directory).toBe('alpha');
    const again = await service.installFromLocalZip(await readFixture('multi-skill.zip'), { skill: 'beta' });
    expect(again.id).toBe(beta.id); // 同为本地来源 → 复用刷新同一行
    // repo 来源想占用本地已装的 beta 目录名 → 拒绝
    stubGithub(
      'https://github.com/other/repo/archive/refs/heads/main.zip',
      makeZip([{ name: 'beta/SKILL.md', content: '---\nname: beta\n---\nother repo\n' }]),
    );
    await expect(service.installFromRepo('other/repo', { skill: 'beta' })).rejects.toThrow(/已被/);
  });

  it('集合安装 ZIP：multi-skill.zip 一次收编全部；重装刷新复用', async () => {
    const report = await service.installCollectionFromLocalZip(await readFixture('multi-skill.zip'));
    expect(report.failed).toEqual([]);
    expect(report.installed.map((s) => s.directory).sort()).toEqual(['alpha', 'beta']);

    const again = await service.installCollectionFromLocalZip(await readFixture('multi-skill.zip'));
    expect(again.failed).toEqual([]);
    expect(again.installed).toHaveLength(2); // 同来源 → 刷新复用同一批 SSOT 行
  });

  it('集合安装整仓：一次归档收编全部技能；同仓重装幂等刷新', async () => {
    stubGithub(
      'https://github.com/obra/superpowers/archive/refs/heads/main.zip',
      makeZip([
        { name: 'superpowers-main/skills/alpha/SKILL.md', content: '---\nname: alpha\n---\nA' },
        { name: 'superpowers-main/skills/beta/SKILL.md', content: '---\nname: beta\n---\nB' },
        { name: 'superpowers-main/README.md', content: 'readme' },
      ]),
    );
    const report = await service.installCollectionFromRepo('obra/superpowers');
    expect(report.failed).toEqual([]);
    expect(report.installed.map((s) => s.directory).sort()).toEqual(['alpha', 'beta']);
    expect(report.installed.every((s) => s.repoOwner === 'obra' && s.repoName === 'superpowers')).toBe(true);

    const again = await service.installCollectionFromRepo('obra/superpowers');
    expect(again.failed).toEqual([]);
    expect(again.installed).toHaveLength(2); // 同来源 → 刷新复用同一批 SSOT 行
  });

  it('集合安装整仓：跨来源目录冲突的技能跳过不中断，其余照常收编', async () => {
    // 本地先占用 alpha 目录
    const local = await service.installFromLocalZip(await readFixture('multi-skill.zip'), { skill: 'alpha' });
    stubGithub(
      'https://github.com/obra/superpowers/archive/refs/heads/main.zip',
      makeZip([
        { name: 'superpowers-main/skills/alpha/SKILL.md', content: '---\nname: alpha\n---\nA' },
        { name: 'superpowers-main/skills/beta/SKILL.md', content: '---\nname: beta\n---\nB' },
      ]),
    );
    const report = await service.installCollectionFromRepo('obra/superpowers');
    expect(report.installed.map((s) => s.directory)).toEqual(['beta']);
    expect(report.failed).toEqual([{ directory: 'alpha', reason: expect.stringMatching(/已被/) }]);
    // 冲突未影响既有数据：本地 alpha 原样保留
    expect((await service.list()).find((s) => s.id === local.id)?.directory).toBe('alpha');
  });

  it('zip-slip 与 symlink 条目拒收', async () => {
    // yauzl 对 `..` 条目先行拒绝（invalid relative path），我们的越界防线是第二层兜底
    await expect(service.installFromLocalZip(await readFixture('evil-slip.zip'))).rejects.toThrow(
      /越界|invalid relative path/,
    );
    await expect(service.installFromLocalZip(await readFixture('evil-link.zip'))).rejects.toThrow(/symlink/);
  });
});

describe('U-SK-04 更新检查时序', () => {
  it('SSOT 目录缺失 → 必报更新（不信缓存哈希）', async () => {
    const row = await service.installFromLocalZip(await readFixture('demo-skill.zip'));
    await fs.rm(path.join(store.homeDir, 'skills', 'demo-skill'), { recursive: true, force: true });
    row.repoOwner = 'demo';
    row.repoName = 'skill';
    row.branch = 'main';
    stubGithub('https://github.com/demo/skill/archive/refs/heads/main.zip', makeZip([{ name: 'demo-skill/SKILL.md', content: '---\nname: demo-skill\n---\nremote\n' }]));

    const report = await service.checkUpdates();
    expect(report.failures).toHaveLength(0);
    expect(report.updates).toHaveLength(1);
    expect(report.updates[0]!.missing).toBe(true);
    expect(report.updates[0]!.skillId).toBe(row.id);
  });

  it('远端有更新 → update 应用并重投影已启用目标；installed_at 前滚；留备份', async () => {
    const row = await service.installFromLocalZip(await readFixture('demo-skill.zip'));
    await service.toggle(row.id, 'claude-code', true);
    row.repoOwner = 'demo';
    row.repoName = 'skill';
    row.branch = 'main';
    const oldHash = row.contentHash;
    const oldInstalledAt = row.installedAt;
    stubGithub('https://github.com/demo/skill/archive/refs/heads/main.zip', makeZip([{ name: 'demo-skill/SKILL.md', content: '---\nname: demo-skill\n---\nnew body\n' }]));

    const updated = await service.update(row.id);
    expect(updated.contentHash).not.toBe(oldHash);
    expect(updated.installedAt).not.toBe(oldInstalledAt);
    const projected = path.join(sandbox, '.claude', 'skills', 'demo-skill');
    expect((await fs.readFile(path.join(projected, 'SKILL.md'), 'utf8')).includes('new body')).toBe(true);
    const backups = await fs.readdir(path.join(store.homeDir, 'skill-backups'));
    expect(backups.length).toBeGreaterThanOrEqual(1);
  });
});

describe('U-SK-05 skills.sh 客户端', () => {
  it('source 过 GitHub 白名单；分支解析；去重', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(
          JSON.stringify({
            count: 3,
            skills: [
              { skillId: 'find-skills', name: 'find-skills', installs: 1000, source: 'anthropics/skills' },
              { skillId: 'x', name: 'x', installs: 5, source: 'owner/repo@dev' },
              { skillId: 'bad', name: 'bad', installs: 9, source: 'skills.volces.com/x' },
              { skillId: 'find-skills', name: 'find-skills', installs: 1000, source: 'anthropics/skills' },
            ],
          }),
          { status: 200 },
        ),
      ) as unknown as typeof fetch,
    );
    const res = await searchSkillsSh('find');
    expect(res.count).toBe(3);
    expect(res.skills).toHaveLength(2);
    expect(res.skills[0]).toMatchObject({ repo: 'anthropics/skills', branch: 'main', installs: 1000 });
    expect(res.skills[1]).toMatchObject({ repo: 'owner/repo', branch: 'dev' });
  });
});

describe('U-SK-06 4 级解析', () => {
  it('同名唯一目录优先；metadata name 唯一匹配兜底', async () => {
    const root = path.join(sandbox, 'repo');
    await fs.mkdir(path.join(root, 'skills', 'alpha'), { recursive: true });
    await fs.mkdir(path.join(root, 'nested', 'beta'), { recursive: true });
    await fs.writeFile(path.join(root, 'skills', 'alpha', 'SKILL.md'), '---\nname: alpha\n---\n');
    await fs.writeFile(path.join(root, 'nested', 'beta', 'SKILL.md'), '---\nname: beta\n---\n');
    const scanned = await scanSkillDirs(root);
    expect(scanned).toHaveLength(2);
    expect(path.basename(resolveSkillSourceDir(root, scanned, 'beta')!)).toBe('beta');
    expect(path.basename(resolveSkillSourceDir(root, scanned, 'alpha')!)).toBe('alpha');
    expect(resolveSkillSourceDir(root, scanned, 'nope')).toBeNull();
  });
});

// —— 辅助：无依赖最小 ZIP 构造（stored）+ fetch stub ——

function crc32(buf: Buffer): number {
  const table = crcTable;
  let c = 0xffffffff;
  for (let i = 0; i < buf.length; i++) {
    c = (c >>> 8) ^ table[(c ^ buf[i]!) & 0xff]!;
  }
  return (c ^ 0xffffffff) >>> 0;
}
const crcTable = (() => {
  const t: number[] = [];
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();

function makeZip(files: Array<{ name: string; content: string }>): Buffer {
  const chunks: Buffer[] = [];
  const central: Buffer[] = [];
  let offset = 0;
  for (const f of files) {
    const nameBuf = Buffer.from(f.name, 'utf8');
    const data = Buffer.from(f.content, 'utf8');
    const crc = crc32(data);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0, 8); // stored
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(nameBuf.length, 26);
    chunks.push(local, nameBuf, data);
    const cen = Buffer.alloc(46);
    cen.writeUInt32LE(0x02014b50, 0);
    cen.writeUInt16LE(20, 4);
    cen.writeUInt16LE(20, 6);
    cen.writeUInt16LE(0, 10);
    cen.writeUInt32LE(crc, 16);
    cen.writeUInt32LE(data.length, 20);
    cen.writeUInt32LE(data.length, 24);
    cen.writeUInt16LE(nameBuf.length, 28);
    cen.writeUInt32LE(offset, 42);
    central.push(cen, nameBuf);
    offset += local.length + nameBuf.length + data.length;
  }
  const centralBuf = Buffer.concat(central);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(files.length, 8);
  end.writeUInt16LE(files.length, 10);
  end.writeUInt32LE(centralBuf.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...chunks, centralBuf, end]);
}

/** stub 全局 fetch：github 归档 URL → 内存 ZIP；其他 URL 走真实现 */
function stubGithub(archiveUrl: string, zip: Buffer): void {
  const real = globalThis.fetch;
  const repoPrefix = archiveUrl.replace(/\/archive\/refs\/heads\/.*$/, '');
  vi.stubGlobal('fetch', (async (input: Parameters<typeof fetch>[0]) => {
    const url = String(input);
    if (url.includes(`${repoPrefix}/archive/refs/heads/`)) {
      return new Response(new Uint8Array(zip), { status: 200 });
    }
    return real(input);
  }) as unknown as typeof fetch);
}
