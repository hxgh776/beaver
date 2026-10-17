import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { computeDirHash } from './hash.js';
import {
  SkillInputError,
  githubArchiveUrl,
  searchSkillsSh,
  skillDirsForAgents,
  validateRepoRef,
} from './skills-sh.js';
import { downloadZip, extractZip } from './zip.js';
import type {
  InstalledSkill,
  SkillAgentType,
  SkillProjectionStatus,
  SkillRepoFailure,
  SkillSearchResult,
  SkillSource,
  SkillUpdateCandidate,
  SkillUpdateReport,
  SkillWithStatus,
} from './types.js';
import { SKILL_AGENT_LIST } from './types.js';
import type { ManagerConfig } from '../types.js';
import type { Store } from '../store.js';

export interface SkillsServiceOptions {
  store: Store;
  /** 返回当前共享配置对象（daemon 持有单一实例） */
  getConfig(): ManagerConfig;
  saveConfig(): Promise<void>;
  /** agent 主目录（测试传沙箱根；缺省用户主目录） */
  agentRoot?: string;
}

const SKILL_MD = 'SKILL.md';
const SCAN_DEPTH = 6;
const BACKUP_KEEP = 20;

/** 集合安装结果：一次归档（仓库/ZIP）收编多个技能 */
export interface CollectionInstallReport {
  installed: InstalledSkill[];
  failed: Array<{ directory: string; reason: string }>;
}

export class SkillsService {
  private readonly ssotDir: string;
  private readonly targetDirs: Record<SkillAgentType, string>;

  constructor(private readonly opts: SkillsServiceOptions) {
    this.ssotDir = path.join(opts.store.homeDir, 'skills');
    this.targetDirs = skillDirsForAgents(opts.agentRoot);
  }

  // —— 查询 ——

  get skillsDir(): string {
    return this.ssotDir;
  }

  skillRow(id: string): InstalledSkill {
    const row = this.opts.getConfig().skills.find((s) => s.id === id);
    if (!row) throw new SkillInputError(`未安装的 skill: ${id}`);
    return row;
  }

  async search(q: string, limit = 20, offset = 0): Promise<SkillSearchResult> {
    return searchSkillsSh(q, limit, offset);
  }

  async list(): Promise<SkillWithStatus[]> {
    const out: SkillWithStatus[] = [];
    for (const row of this.opts.getConfig().skills) {
      out.push({ ...row, targets: await this.projectionStatus(row) });
    }
    return out;
  }

  private async projectionStatus(row: InstalledSkill): Promise<SkillProjectionStatus[]> {
    const ssotHash = await this.ssotHash(row);
    const enabledBy = new Map(
      this.opts
        .getConfig()
        .skillTargets.filter((t) => t.skillId === row.id)
        .map((t) => [t.agentType, t.enabled]),
    );
    const status: SkillProjectionStatus[] = [];
    for (const agentType of SKILL_AGENT_LIST) {
      const dir = path.join(this.targetDirs[agentType], row.directory);
      const exists = await existsDir(dir);
      let projected = false;
      let modified = false;
      if (exists) {
        if (ssotHash === null) {
          modified = true; // SSOT 缺失但目标在——按用户自维护对待
        } else {
          const h = await computeDirHash(dir);
          projected = h === ssotHash;
          modified = !projected;
        }
      }
      status.push({ agentType, enabled: enabledBy.get(agentType) ?? false, projected, modified });
    }
    return status;
  }

  private async ssotHash(row: InstalledSkill): Promise<string | null> {
    const dir = path.join(this.ssotDir, row.directory);
    if (!(await existsDir(dir))) return null;
    return computeDirHash(dir);
  }

  // —— 安装 ——

  async installFromRepo(
    repo: string,
    opts: { skill?: string; branch?: string; enableFor?: SkillAgentType[] } = {},
  ): Promise<InstalledSkill> {
    const { owner, name, branch: explicitBranch } = validateRepoRef(repo, opts.branch ?? 'main');
    const branch = explicitBranch ?? (await this.resolveBranch(owner, name));
    const tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-skillzip-'));
    try {
      const { sourceDir, rootName } = await this.fetchAndLocate(tmp, owner, name, branch, opts.skill);
      const row = await this.admit({
        source: 'github',
        directoryHint: opts.skill,
        sourceDir,
        repo: { owner, name, branch },
        readmeUrl: `https://github.com/${owner}/${name}/tree/${branch}/${repoRel(rootName, sourceDir)}`,
        enableFor: opts.enableFor ?? [],
      });
      return row;
    } finally {
      await fs.rm(tmp, { recursive: true, force: true }).catch(() => {});
    }
  }

  async installFromLocalZip(
    zip: Buffer,
    opts: { skill?: string; enableFor?: SkillAgentType[] } = {},
  ): Promise<InstalledSkill> {
    const tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-skillzip-'));
    try {
      await extractZip(zip, tmp);
      const rootName = await singleRootDir(tmp);
      const root = rootName ? path.join(tmp, rootName) : tmp;
      const scanned = await scanSkillDirs(root);
      const sourceDir = resolveSkillSourceDir(root, scanned, opts.skill);
      if (!sourceDir) throw new SkillInputError('ZIP 中未找到包含 SKILL.md 的目录');
      return await this.admit({
        source: 'local',
        directoryHint: opts.skill,
        sourceDir,
        repo: undefined,
        readmeUrl: undefined,
        enableFor: opts.enableFor ?? [],
      });
    } finally {
      await fs.rm(tmp, { recursive: true, force: true }).catch(() => {});
    }
  }

  /** 集合安装报告：installed 与 failed 互斥；单个技能失败（如跨来源目录名冲突）不中断整批 */
  async installCollectionFromRepo(
    repo: string,
    opts: { branch?: string; enableFor?: SkillAgentType[] } = {},
  ): Promise<CollectionInstallReport> {
    const { owner, name, branch: explicitBranch } = validateRepoRef(repo, opts.branch ?? 'main');
    const branch = explicitBranch ?? (await this.resolveBranch(owner, name));
    const tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-skillzip-'));
    try {
      const zip = await downloadZip(githubArchiveUrl(owner, name, branch));
      const extractDir = path.join(tmp, 'x');
      await extractZip(zip, extractDir);
      const rootName = await singleRootDir(extractDir);
      const root = rootName ? path.join(extractDir, rootName) : extractDir;
      const scanned = await scanSkillDirs(root);
      if (scanned.length === 0) {
        throw new SkillInputError(`仓库 ${owner}/${name}@${branch} 中未找到 SKILL.md 目录`);
      }
      return await this.admitAll(
        scanned.map((s) => ({
          hint: collectionHint(s, root, name),
          sourceDir: s.dir,
          readmeUrl: `https://github.com/${owner}/${name}/tree/${branch}/${repoRel(rootName, s.dir)}`,
        })),
        { source: 'github', repo: { owner, name, branch }, enableFor: opts.enableFor ?? [] },
      );
    } finally {
      await fs.rm(tmp, { recursive: true, force: true }).catch(() => {});
    }
  }

  async installCollectionFromLocalZip(
    zip: Buffer,
    opts: { enableFor?: SkillAgentType[] } = {},
  ): Promise<CollectionInstallReport> {
    const tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-skillzip-'));
    try {
      await extractZip(zip, tmp);
      const rootName = await singleRootDir(tmp);
      const root = rootName ? path.join(tmp, rootName) : tmp;
      const scanned = await scanSkillDirs(root);
      if (scanned.length === 0) throw new SkillInputError('ZIP 中未找到包含 SKILL.md 的目录');
      return await this.admitAll(
        scanned.map((s) => ({ hint: collectionHint(s, root, rootName ?? 'skill'), sourceDir: s.dir })),
        { source: 'local', enableFor: opts.enableFor ?? [] },
      );
    } finally {
      await fs.rm(tmp, { recursive: true, force: true }).catch(() => {});
    }
  }

  /** 集合收编循环：同名目录去重（首个胜出，scan 按路径排序保证确定性）；单个失败收集进 failed 继续装 */
  private async admitAll(
    entries: Array<{ hint: string; sourceDir: string; readmeUrl?: string }>,
    common: { source: SkillSource; repo?: { owner: string; name: string; branch: string }; enableFor: SkillAgentType[] },
  ): Promise<CollectionInstallReport> {
    const installed: InstalledSkill[] = [];
    const failed: Array<{ directory: string; reason: string }> = [];
    const seen = new Set<string>();
    for (const e of entries) {
      const dirName = sanitizeInstallName(e.hint);
      if (seen.has(dirName)) continue;
      seen.add(dirName);
      try {
        installed.push(
          await this.admit({
            source: common.source,
            directoryHint: e.hint,
            sourceDir: e.sourceDir,
            repo: common.repo,
            readmeUrl: e.readmeUrl,
            enableFor: common.enableFor,
          }),
        );
      } catch (err) {
        failed.push({ directory: dirName, reason: err instanceof Error ? err.message : String(err) });
      }
    }
    return { installed, failed };
  }

  /** 分支回退：main → master（skill.rs:3545-3577） */
  private async resolveBranch(owner: string, name: string): Promise<string> {
    let lastError: unknown;
    for (const branch of ['main', 'master']) {
      try {
        validateRepoRef(`${owner}/${name}`, branch);
        await downloadZip(githubArchiveUrl(owner, name, branch)); // 探测下载即校验存在性
        return branch;
      } catch (err) {
        lastError = err;
      }
    }
    throw lastError instanceof Error ? lastError : new SkillInputError('无法确定分支');
  }

  private async fetchAndLocate(
    tmpRoot: string,
    owner: string,
    name: string,
    branch: string,
    skill?: string,
  ): Promise<{ sourceDir: string; rootName: string | null }> {
    const zip = await downloadZip(githubArchiveUrl(owner, name, branch));
    const extractDir = path.join(tmpRoot, 'x');
    await extractZip(zip, extractDir);
    const rootName = await singleRootDir(extractDir);
    const root = rootName ? path.join(extractDir, rootName) : extractDir;
    const scanned = await scanSkillDirs(root);
    const sourceDir = resolveSkillSourceDir(root, scanned, skill);
    if (!sourceDir) {
      throw new SkillInputError(
        `仓库 ${owner}/${name}@${branch} 中未找到${skill ? ` ${skill} 对应的` : ''} SKILL.md 目录`,
      );
    }
    return { sourceDir, rootName };
  }

  /** 把解析出的源目录收编进 SSOT（冲突检查 → 复制 → 哈希 → 落库 → 按需投影） */
  private async admit(input: {
    source: SkillSource;
    directoryHint?: string;
    sourceDir: string;
    repo?: { owner: string; name: string; branch: string };
    readmeUrl?: string;
    enableFor: SkillAgentType[];
  }): Promise<InstalledSkill> {
    const config = this.opts.getConfig();
    const metadata = await parseSkillMetadata(path.join(input.sourceDir, SKILL_MD));
    const directory = sanitizeInstallName(
      input.directoryHint ?? metadata?.name ?? input.repo?.name ?? 'skill',
    );
    // 复用/冲突判定：同来源（同 repo 坐标，或同为本地无坐标）→ 刷新；异来源占用同目录名 → 报错不覆盖（#3749 语义）
    const existing = config.skills.find((s) => s.directory === directory);
    if (existing) {
      const sameRepo = input.repo
        ? existing.repoOwner === input.repo.owner &&
          existing.repoName === input.repo.name &&
          existing.branch === input.repo.branch
        : !existing.repoOwner && !existing.repoName && existing.source === input.source;
      if (!sameRepo) {
        throw new SkillInputError(
          `目录名 ${directory} 已被${existing.repoOwner ? ` ${existing.repoOwner}/${existing.repoName}` : '本地/其他来源'}占用`,
        );
      }
    }
    const ssotTarget = path.join(this.ssotDir, directory);
    await copyDir(input.sourceDir, ssotTarget);
    const contentHash = await computeDirHash(ssotTarget);
    const now = new Date().toISOString();
    const slug = skillSlug(metadata?.name ?? directory) || directory.toLowerCase().replace(/[^a-z0-9-]+/g, '-');
    const id = existing?.id ?? `skill-${slug}`;

    let row: InstalledSkill;
    if (existing) {
      Object.assign(existing, {
        name: metadata?.name ?? existing.name,
        description: metadata?.description,
        source: input.source,
        repoOwner: input.repo?.owner,
        repoName: input.repo?.name,
        branch: input.repo?.branch,
        readmeUrl: input.readmeUrl,
        contentHash,
        updatedAt: now,
        installedAt: now, // 乐观锁前滚：重装视同新装
      } satisfies Partial<InstalledSkill>);
      row = existing;
    } else {
      row = {
        id,
        directory,
        name: metadata?.name ?? directory,
        description: metadata?.description,
        source: input.source,
        repoOwner: input.repo?.owner,
        repoName: input.repo?.name,
        branch: input.repo?.branch,
        readmeUrl: input.readmeUrl,
        contentHash,
        installedAt: now,
        updatedAt: now,
      };
      config.skills.push(row);
      // 关联表默认全关：启用是显式动作（与 MCP 分发语义一致）
      for (const agentType of SKILL_AGENT_LIST) {
        config.skillTargets.push({ skillId: id, agentType, enabled: false });
      }
    }
    for (const agentType of input.enableFor) {
      await this.enableTarget(row, agentType);
      // 投影即启用：把关联位一并置 true，否则 UI 显示未启用（与 toggle(true) 语义对齐）
      const target = config.skillTargets.find((x) => x.skillId === row.id && x.agentType === agentType);
      if (target) target.enabled = true;
    }
    await this.opts.saveConfig();
    return row;
  }

  // —— 投影（永远 copy，不做 symlink；Windows symlink 防御整套省掉）——

  async toggle(skillId: string, agentType: SkillAgentType, enabled: boolean): Promise<{ preserved?: string }> {
    const config = this.opts.getConfig();
    const row = this.skillRow(skillId);
    // 旧数据可能没有新增 agent 的关联行（skill agent 名单后来扩充）——懒建，而非报错
    let target = config.skillTargets.find((t) => t.skillId === skillId && t.agentType === agentType);
    if (!target) {
      target = { skillId, agentType, enabled: false };
      config.skillTargets.push(target);
    }
    if (target.enabled === enabled) return {};
    let preserved: string | undefined;
    if (enabled) {
      await this.enableTarget(row, agentType);
    } else {
      preserved = await this.disableTarget(row, agentType);
    }
    // 无论目标目录是否被保留，启用位都翻转到用户请求的状态
    target.enabled = enabled;
    await this.opts.saveConfig();
    return preserved ? { preserved } : {};
  }

  private async enableTarget(row: InstalledSkill, agentType: SkillAgentType): Promise<void> {
    const ssot = path.join(this.ssotDir, row.directory);
    if (!(await existsDir(ssot))) throw new SkillInputError(`SSOT 目录缺失: ${row.directory}`);
    const ssotHash = await computeDirHash(ssot);
    const dest = path.join(this.targetDirs[agentType], row.directory);
    if (await existsDir(dest)) {
      const h = await computeDirHash(dest);
      if (h === ssotHash) return; // 已投影且一致 → 幂等
      throw new SkillInputError(
        `目标已存在且内容不同，拒绝覆盖: ${dest}（如确认替换请先卸载旧目录）`,
      );
    }
    await replaceWithCopy(ssot, dest);
  }

  private async disableTarget(row: InstalledSkill, agentType: SkillAgentType): Promise<string | undefined> {
    const ssotHash = await this.ssotHash(row);
    const dest = path.join(this.targetDirs[agentType], row.directory);
    if (!(await existsDir(dest))) return undefined;
    const h = await computeDirHash(dest);
    // 只删"能证明是我们投影的"目录；用户改过的原样保留（所有权守卫）
    if (ssotHash !== null && h === ssotHash) {
      await fs.rm(dest, { recursive: true, force: true });
      return undefined;
    }
    return dest;
  }

  // —— 更新检查 ——

  async checkUpdates(): Promise<SkillUpdateReport> {
    const config = this.opts.getConfig();
    const byRepo = new Map<string, InstalledSkill[]>();
    for (const row of config.skills) {
      if (!row.repoOwner || !row.repoName) continue;
      const key = `${row.repoOwner}/${row.repoName}@${row.branch ?? 'main'}`;
      byRepo.set(key, [...(byRepo.get(key) ?? []), row]);
    }
    const updates: SkillUpdateCandidate[] = [];
    const failures: SkillRepoFailure[] = [];
    for (const [key, rows] of byRepo) {
      const at = key.lastIndexOf('@');
      const branch = key.slice(at + 1);
      let tmpRoot: string | null = null;
      try {
        tmpRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-skillchk-'));
        const { sourceDir, rootName } = await this.fetchAndLocate(tmpRoot, rows[0]!.repoOwner!, rows[0]!.repoName!, branch);
        const remoteRoot = rootName ? path.join(path.dirname(sourceDir), rootName) : sourceDir;
        const scanned = await scanSkillDirs(remoteRoot);
        for (const row of rows) {
          const ssotDir = path.join(this.ssotDir, row.directory);
          let localHash: string | null;
          try {
            await fs.access(ssotDir);
            localHash = await computeDirHash(ssotDir); // 先确认目录存在再算（时序铁律）
          } catch {
            localHash = null;
          }
          const remote = matchRemoteSkill(scanned, row);
          if (!remote) continue;
          const remoteHash = await computeDirHash(remote.dir);
          if (localHash === null || remoteHash !== localHash) {
            updates.push({
              skillId: row.id,
              directory: row.directory,
              repo: key,
              localHash: localHash ?? 'missing',
              remoteHash,
              missing: localHash === null,
            });
          } else if (localHash !== row.contentHash) {
            row.contentHash = localHash; // 现场算出的哈希回填（skill.rs:1287-1326）
          }
        }
      } catch (err) {
        failures.push({ repo: key, reason: err instanceof Error ? err.message : String(err) });
      } finally {
        if (tmpRoot) await fs.rm(tmpRoot, { recursive: true, force: true }).catch(() => {});
      }
    }
    await this.opts.saveConfig().catch(() => {});
    return { updates, failures, checkedAt: new Date().toISOString() };
  }

  /** 应用单个更新：备份旧目录 → 换 SSOT → 重投影我们自己的旧投影（skill.rs:1496-1688） */
  async update(skillId: string): Promise<InstalledSkill> {
    const row = this.skillRow(skillId);
    if (!row.repoOwner || !row.repoName) throw new SkillInputError('该 skill 无 GitHub 来源，无法更新');
    const branch = row.branch ?? 'main';
    const tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-skillupd-'));
    try {
      const { sourceDir } = await this.fetchAndLocate(tmp, row.repoOwner, row.repoName, branch, row.directory);
      const metadata = await parseSkillMetadata(path.join(sourceDir, SKILL_MD));
      const newHash = await computeDirHash(sourceDir);
      const ssot = path.join(this.ssotDir, row.directory);
      const oldHash = await this.ssotHash(row);
      await this.backupSkill(row);
      if (await existsDir(ssot)) await fs.rm(ssot, { recursive: true, force: true });
      await copyDir(sourceDir, ssot);
      // 重新投影：目标哈希等于旧内容 → 是我们的投影，替换；否则保留用户改动
      for (const t of this.opts.getConfig().skillTargets.filter((x) => x.skillId === skillId && x.enabled)) {
        const dest = path.join(this.targetDirs[t.agentType], row.directory);
        if (!(await existsDir(dest))) {
          await replaceWithCopy(ssot, dest);
          continue;
        }
        const h = await computeDirHash(dest);
        if (h === newHash) continue;
        if (oldHash !== null && h === oldHash) {
          await fs.rm(dest, { recursive: true, force: true });
          await replaceWithCopy(ssot, dest);
        }
      }
      row.contentHash = newHash;
      row.name = metadata?.name ?? row.name;
      row.description = metadata?.description;
      row.installedAt = new Date().toISOString(); // 乐观锁前滚
      row.updatedAt = row.installedAt;
      await this.opts.saveConfig();
      return row;
    } finally {
      await fs.rm(tmp, { recursive: true, force: true }).catch(() => {});
    }
  }

  // —— 卸载 ——

  async uninstall(skillId: string): Promise<{ preserved: string[]; backup: string | null }> {
    const config = this.opts.getConfig();
    const row = this.skillRow(skillId);
    // 目录名必须仍是 sanitize 形态——脏行跳过文件操作但仍删注册行（skill.rs:1046-1053）
    const safeDir = sanitizeInstallName(row.directory) === row.directory ? row.directory : null;
    const preserved: string[] = [];
    let backup: string | null = null;
    if (safeDir) {
      backup = await this.backupSkill(row);
      for (const agentType of SKILL_AGENT_LIST) {
        const p = await this.disableTarget(row, agentType);
        if (p && !preserved.includes(p)) preserved.push(p);
      }
      const ssot = path.join(this.ssotDir, safeDir);
      if (await existsDir(ssot)) await fs.rm(ssot, { recursive: true, force: true });
    }
    config.skills = config.skills.filter((s) => s.id !== skillId);
    config.skillTargets = config.skillTargets.filter((t) => t.skillId !== skillId);
    await this.opts.saveConfig();
    return { preserved, backup };
  }

  /** 卸载/更新前整目录备份：skill-backups/<ts>_<slug>/{skill/, meta.json}，保留 20 份 */
  private async backupSkill(row: InstalledSkill): Promise<string | null> {
    const source = path.join(this.ssotDir, row.directory);
    let from = (await existsDir(source)) ? source : null;
    if (!from) {
      // SSOT 缺失时从任一投影回捞（skill.rs:3859-3891）
      for (const agentType of SKILL_AGENT_LIST) {
        const dest = path.join(this.targetDirs[agentType], row.directory);
        if (await existsDir(dest)) {
          from = dest;
          break;
        }
      }
    }
    if (!from) return null;
    const root = path.join(this.opts.store.homeDir, 'skill-backups');
    const ts = new Date().toISOString().replaceAll(/[:.]/g, '-');
    const dest = path.join(root, `${ts}_${row.directory}`);
    await fs.mkdir(dest, { recursive: true });
    await copyDir(from, path.join(dest, 'skill'));
    await fs.writeFile(
      path.join(dest, 'meta.json'),
      JSON.stringify({ ...row, backedUpAt: new Date().toISOString() }, null, 2),
    );
    await pruneBackups(root, BACKUP_KEEP);
    return dest;
  }
}

// —— 纯工具 ——

async function existsDir(dir: string): Promise<boolean> {
  try {
    const st = await withRetry(() => fs.stat(dir));
    return st.isDirectory();
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === 'ENOENT') return false;
    throw err;
  }
}

async function copyDir(src: string, dest: string): Promise<void> {
  await fs.mkdir(path.dirname(dest), { recursive: true });
  await fs.cp(src, dest, { recursive: true });
}

/** copy → 同父目录 staging → rename，保证目标替换的原子性（skill.rs:2691-2730）。
 * Windows 上 rename 可能被杀软/索引器短暂占用目标而抛 EPERM/EBUSY——指数退避重试（与 atomicWriteFile 同策略）。 */
async function replaceWithCopy(src: string, dest: string): Promise<void> {
  await fs.mkdir(path.dirname(dest), { recursive: true });
  const staging = path.join(
    path.dirname(dest),
    `.${path.basename(dest)}.tmp-${process.pid}-${Math.random().toString(36).slice(2, 8)}`,
  );
  try {
    await fs.cp(src, staging, { recursive: true });
    await fs.rm(dest, { recursive: true, force: true });
    let lastError: unknown;
    for (let attempt = 0; attempt < 5; attempt++) {
      try {
        await fs.rename(staging, dest);
        return;
      } catch (err) {
        lastError = err;
        const code = (err as NodeJS.ErrnoException).code;
        const transient = code === 'EPERM' || code === 'EBUSY' || code === 'EACCES';
        if (!transient || attempt === 4) break;
        await new Promise((resolve) => setTimeout(resolve, 10 * 2 ** attempt));
      }
    }
    throw lastError;
  } catch (err) {
    await fs.rm(staging, { recursive: true, force: true }).catch(() => {});
    throw err;
  }
}

async function pruneBackups(root: string, keep: number): Promise<void> {
  const entries = (await fs.readdir(root)).sort();
  if (entries.length <= keep) return;
  for (const name of entries.slice(0, entries.length - keep)) {
    await fs.rm(path.join(root, name), { recursive: true, force: true });
  }
}

/** 安装名 sanitize：跨平台显式禁两个分隔符，拒绝 `..` 与首尾点（skill.rs:3181-3185） */
export function sanitizeInstallName(raw: string): string {
  const cleaned = raw
    .split('\\').join('/')
    .split('/').join('-')
    .replace(/[^A-Za-z0-9._-]+/g, '-')
    .replace(/\.\.+/g, '.')
    .replace(/^[-.]+/, '')
    .slice(0, 80);
  if (!cleaned || cleaned === '.' || cleaned === '..') return 'skill';
  return cleaned;
}

export function skillSlug(raw: string): string {
  return (
    raw
      .toLowerCase()
      .replace(/[^a-z0-9-]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 60) || ''
  );
}

export interface ScannedSkillDir {
  dir: string;
  metadata: { name?: string; description?: string } | null;
}

/** 递归扫描含 SKILL.md 的目录（深度 ≤6）；根目录本身含 SKILL.md 也算；返回按路径排序的确定性列表 */
export async function scanSkillDirs(root: string): Promise<ScannedSkillDir[]> {
  const out: ScannedSkillDir[] = [];
  if (await exists(path.join(root, SKILL_MD))) {
    out.push({ dir: root, metadata: await parseSkillMetadata(path.join(root, SKILL_MD)) });
  }
  const walk = async (dir: string, depth: number): Promise<void> => {
    if (depth > SCAN_DEPTH) return;
    let entries;
    try {
      // Windows 上刚解压完的目录可能被杀软/索引器短暂占用（EPERM/EBUSY），小退避重试
      entries = await withRetry(() => fs.readdir(dir, { withFileTypes: true }));
    } catch {
      return;
    }
    for (const e of entries) {
      if (!e.isDirectory() || e.name.startsWith('.')) continue;
      const full = path.join(dir, e.name);
      if (await exists(path.join(full, SKILL_MD))) {
        out.push({ dir: full, metadata: await parseSkillMetadata(path.join(full, SKILL_MD)) });
      }
      await walk(full, depth + 1);
    }
  };
  await walk(root, 0);
  return out.sort((a, b) => (a.dir < b.dir ? -1 : 1));
}

async function exists(p: string): Promise<boolean> {
  try {
    await withRetry(() => fs.access(p));
    return true;
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === 'ENOENT') return false;
    throw err;
  }
}

/** Windows 瞬时文件锁（EPERM/EBUSY/EACCES）退避重试——与 atomicWriteFile 的 rename 重试同理 */
async function withRetry<T>(fn: () => Promise<T>, attempts = 6): Promise<T> {
  let lastError: unknown;
  for (let i = 0; i < attempts; i++) {
    try {
      return await fn();
    } catch (err) {
      lastError = err;
      const code = (err as NodeJS.ErrnoException).code;
      if (code !== 'EPERM' && code !== 'EBUSY' && code !== 'EACCES') throw err;
      await new Promise((r) => setTimeout(r, 10 * 2 ** i));
    }
  }
  throw lastError;
}

async function singleRootDir(root: string): Promise<string | null> {
  const entries = await fs.readdir(root, { withFileTypes: true });
  const dirs = entries.filter((e) => e.isDirectory()).map((e) => e.name);
  return dirs.length === 1 && entries.length === 1 ? dirs[0]! : null;
}

/**
 * 4 级解析（skill.rs:3411-3470）：递归同名唯一目录 → 直接路径 → metadata name 唯一匹配 →
 * 多个同名取第一个；未指定名时：唯一 skill 或根目录 SKILL.md 兜底，否则取第一个。
 */
export function resolveSkillSourceDir(root: string, scanned: ScannedSkillDir[], skill?: string): string | null {
  if (scanned.length === 0) return null;
  if (!skill) {
    if (scanned.some((s) => path.resolve(s.dir) === path.resolve(root))) return root;
    return scanned[0]!.dir;
  }
  const byDirName = scanned.filter((s) => path.basename(s.dir) === skill);
  if (byDirName.length === 1) return byDirName[0]!.dir;
  const byMeta = scanned.filter((s) => s.metadata?.name === skill);
  if (byMeta.length === 1) return byMeta[0]!.dir;
  return byDirName[0]?.dir ?? byMeta[0]?.dir ?? null;
}

function matchRemoteSkill(scanned: ScannedSkillDir[], row: InstalledSkill): { dir: string } | null {
  const byDir = scanned.find((s) => path.basename(s.dir) === row.directory);
  if (byDir) return { dir: byDir.dir };
  const byMeta = scanned.filter((s) => s.metadata?.name === row.name);
  if (byMeta.length === 1) return { dir: byMeta[0]!.dir };
  return null;
}

/** 极简 frontmatter 解析：--- 块内的 name/description */
export async function parseSkillMetadata(file: string): Promise<{ name?: string; description?: string } | null> {
  let text: string;
  try {
    text = await fs.readFile(file, 'utf8');
  } catch {
    return null;
  }
  if (!text.startsWith('---')) return null;
  const end = text.indexOf('---', 3);
  if (end < 0) return null;
  const meta: { name?: string; description?: string } = {};
  for (const line of text.slice(3, end).split('\n')) {
    const m = line.match(/^(name|description)\s*:\s*(.+)$/);
    if (m) {
      const key = m[1] as 'name' | 'description';
      meta[key] = m[2]!.trim().replace(/^["']|["']$/g, '');
    }
  }
  return meta;
}

function repoRel(rootName: string | null, sourceDir: string): string {
  if (!rootName) return '';
  const norm = sourceDir.split('\\').join('/');
  const idx = norm.indexOf(`/${rootName}/`);
  return idx >= 0 ? norm.slice(idx + rootName.length + 2) : '';
}

/** 集合安装的目录命名：子目录用目录名；根目录自身的 SKILL.md 用 metadata name，兜底仓库名/归档根名 */
function collectionHint(s: ScannedSkillDir, root: string, fallback: string): string {
  return path.resolve(s.dir) === path.resolve(root) ? (s.metadata?.name ?? fallback) : path.basename(s.dir);
}
