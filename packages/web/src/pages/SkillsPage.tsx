import { useMemo, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import type { SkillCollectionReport, SkillSearchResult, SkillUpdateReport, SkillWithStatus } from '../api';
import { Badge, Btn, Card, ConfirmDialog, Icon, Pager, Segmented, Spinner } from '../ui';
import { useToast } from '../ui';

/** 已安装 skill 卡片分页：lg 2 列 × 2 行，配标签行右侧的数字分页 */
const INSTALLED_PAGE_SIZE = 4;

/** skill 投影支持的 agent 展示名（与 core SKILL_AGENT_DIRS 对应；未知类型回退原值） */
const SKILL_AGENT_LABEL: Record<string, string> = {
  'claude-code': 'Claude Code',
  codex: 'Codex CLI',
  'gemini-cli': 'Gemini CLI',
  opencode: 'OpenCode',
  zcode: 'ZCode',
  cursor: 'Cursor',
  windsurf: 'Windsurf',
  trae: 'Trae',
};
const SKILL_AGENT_TYPES = Object.keys(SKILL_AGENT_LABEL);

type TabKey = 'installed' | 'search';

/** 搜索结果 × 已安装的匹配：精确（同仓库 + 同 skill）与仓库级（同仓库装过任意技能） */
function matchInstalled(s: { repo: string; branch: string; skillId: string; name: string }, installed: SkillWithStatus[]) {
  const [owner, name] = s.repo.split('/');
  const sameRepo = installed.filter((r) => r.repoOwner === owner && r.repoName === name);
  const exact = sameRepo.find(
    (r) =>
      (r.branch ?? 'main') === s.branch &&
      (r.directory === s.skillId || r.directory === s.name || r.name === s.skillId || r.name === s.name || r.skillId === s.skillId),
  );
  return { exact, repoCount: sameRepo.length };
}

export function SkillsPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const [q, setQ] = useState('');
  const [searchResult, setSearchResult] = useState<SkillSearchResult | null>(null);
  const [searching, setSearching] = useState(false);
  const [updateReport, setUpdateReport] = useState<SkillUpdateReport | null>(null);
  const [removing, setRemoving] = useState<SkillWithStatus | null>(null);
  const zipInput = useRef<HTMLInputElement>(null);
  const [enableOnInstall, setEnableOnInstall] = useState(true);
  const [tab, setTab] = useState<TabKey>('installed');
  const [installedPage, setInstalledPage] = useState(0);

  const skills = useQuery({
    queryKey: ['skills'],
    queryFn: () => api.get<{ skills: SkillWithStatus[]; ssotDir: string }>('/api/skills'),
  });
  const installed = skills.data?.skills ?? [];

  const invalidate = (): void => {
    void qc.invalidateQueries({ queryKey: ['skills'] });
  };
  const fail = (e: Error): void => {
    toast.err(String(e.message));
  };

  const doSearch = async (): Promise<void> => {
    const kw = q.trim();
    if (kw === '') return;
    setSearching(true);
    try {
      const res = await api.get<SkillSearchResult>(
        // 12 条（3 列 × 4 行）：结果区一屏放下，与 MCP 发现页同规格
        `/api/skills/search?q=${encodeURIComponent(kw)}&limit=12&offset=0`,
      );
      setSearchResult(res);
      setTab('search'); // 搜完直接展示结果，不用再手动切 Tab
    } catch (e) {
      fail(e as Error);
    } finally {
      setSearching(false);
    }
  };

  const enableAll = enableOnInstall ? SKILL_AGENT_TYPES : [];

  const install = useMutation({
    mutationFn: (p: { repo: string; skill?: string }) =>
      api.post<{ skill: SkillWithStatus }>('/api/skills/install', {
        repo: p.repo,
        skill: p.skill,
        enableFor: enableAll,
      }),
    onSuccess: (res) => {
      toast.ok(`已安装 ${res.skill.name}（${enableOnInstall ? '并启用到全部支持 agent' : '未启用'}）`);
      invalidate();
    },
    onError: fail,
  });

  const installZip = useMutation({
    mutationFn: (contentBase64: string) =>
      api.post<{ skill?: SkillWithStatus; report?: SkillCollectionReport }>('/api/skills/install-zip', {
        contentBase64,
        collection: true,
        enableFor: enableAll,
      }),
    onSuccess: (res) => {
      if (res.report !== undefined) {
        const n = res.report.installed.length;
        const f = res.report.failed.length;
        toast.ok(`已从 ZIP 安装 ${n} 个技能${f > 0 ? `，跳过 ${f} 个（${res.report.failed.map((x) => x.directory).join('、')}）` : ''}`);
      } else if (res.skill !== undefined) {
        toast.ok(`已从 ZIP 安装 ${res.skill.name}`);
      }
      invalidate();
    },
    onError: fail,
  });

  // 整仓集合安装：一次下载仓库归档，收编全部 SKILL.md 目录
  const [pendingRepo, setPendingRepo] = useState<string | null>(null);
  const installCollection = useMutation({
    mutationFn: (repo: string) =>
      api.post<SkillCollectionReport>('/api/skills/install-collection', {
        repo,
        enableFor: enableAll,
      }),
    onSuccess: (res, repo) => {
      const n = res.installed.length;
      toast.ok(
        n === 0
          ? `「${repo}」没有可安装的技能（${res.failed.map((x) => x.reason).join('；')}）`
          : `已安装 ${repo} 的 ${n} 个技能${res.failed.length > 0 ? `，跳过 ${res.failed.length} 个（${res.failed.map((x) => x.directory).join('、')}）` : ''}`,
      );
      invalidate();
    },
    onError: fail,
    onSettled: () => setPendingRepo(null),
  });

  const toggle = useMutation({
    mutationFn: (p: { id: string; agentType: string; enabled: boolean }) =>
      api.post<{ preserved?: string }>(`/api/skills/${p.id}/toggle`, p),
    onSuccess: (res) => {
      if (res.preserved) {
        toast.err(`目标目录已被修改，为保护用户内容未删除：${res.preserved}`);
      }
      invalidate();
    },
    onError: fail,
  });

  // 卡片级批量分发：只对与目标状态不一致的 agent 逐个 toggle（服务端幂等，重复点安全）
  const toggleAll = useMutation({
    mutationFn: async (p: { id: string; enabled: boolean; agentTypes: string[] }) => {
      const preserved: string[] = [];
      for (const agentType of p.agentTypes) {
        const res = await api.post<{ preserved?: string }>(`/api/skills/${p.id}/toggle`, {
          agentType,
          enabled: p.enabled,
        });
        if (res.preserved) preserved.push(`${res.preserved}（${agentType}）`);
      }
      return { changed: p.agentTypes.length, preserved };
    },
    onSuccess: (res) => {
      if (res.changed > 0 && res.preserved.length > 0) {
        toast.err(`这些目标目录被修改过，为保护用户内容未删除：${res.preserved.join('、')}`);
      }
      invalidate();
    },
    onError: fail,
  });

  const update = useMutation({
    mutationFn: (id: string) => api.post<{ skill: SkillWithStatus }>(`/api/skills/${id}/update`),
    onSuccess: (res) => {
      toast.ok(`已更新 ${res.skill.name} 并重新分发`);
      invalidate();
    },
    onError: fail,
  });

  const checkUpdates = useMutation({
    mutationFn: () => api.get<SkillUpdateReport>('/api/skills/updates'),
    onSuccess: (res) => {
      setUpdateReport(res);
      invalidate();
    },
    onError: fail,
  });

  const uninstall = useMutation({
    mutationFn: (id: string) => api.del<{ preserved: string[]; backup: string | null }>(`/api/skills/${id}`),
    onSuccess: (res) => {
      toast.ok(`已卸载（备份：${res.backup ?? '无'}）${res.preserved.length > 0 ? `；保留的用户目录：${res.preserved.join('、')}` : ''}`);
      setRemoving(null);
      invalidate();
    },
    onError: fail,
  });

  const onZipPicked = (file: File | undefined): void => {
    if (!file) return;
    const reader = new FileReader();
    reader.onload = (): void => {
      const dataUrl = String(reader.result ?? '');
      const base64 = dataUrl.slice(dataUrl.indexOf(',') + 1);
      installZip.mutate(base64);
    };
    reader.readAsDataURL(file);
  };

  const clearSearch = (): void => {
    setSearchResult(null);
    setQ('');
    setTab('installed');
  };

  const results = searchResult?.skills ?? [];
  const installedTotalPages = Math.max(1, Math.ceil(installed.length / INSTALLED_PAGE_SIZE));
  const safeInstalledPage = Math.min(installedPage, installedTotalPages - 1);
  const pageInstalled = installed.slice(safeInstalledPage * INSTALLED_PAGE_SIZE, (safeInstalledPage + 1) * INSTALLED_PAGE_SIZE);
  const firstRowOfRepo = useMemo(() => {
    const m = new Map<string, string>(); // repo → 首个结果行的 key
    for (const s of results) {
      if (!m.has(s.repo)) m.set(s.repo, s.skillId);
    }
    return m;
  }, [searchResult]);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Btn kind="primary" testId="btn-skills-updates" disabled={checkUpdates.isPending} onClick={() => checkUpdates.mutate()}>
          {checkUpdates.isPending ? <Spinner className="h-4 w-4" /> : '检查更新'}
        </Btn>
        <Btn testId="btn-skills-zip" onClick={() => zipInput.current?.click()} disabled={installZip.isPending}>
          {installZip.isPending ? <Spinner className="h-4 w-4" /> : '安装本地 ZIP'}
        </Btn>
        <input
          ref={zipInput}
          type="file"
          accept=".zip"
          className="hidden"
          data-testid="skill-zip-input"
          onChange={(e) => {
            onZipPicked(e.target.files?.[0]);
            e.target.value = '';
          }}
        />
        <label className="ml-auto flex cursor-pointer items-center gap-2 text-xs text-slate-500">
          <input
            type="checkbox"
            data-testid="skill-enable-on-install"
            checked={enableOnInstall}
            onChange={(e) => setEnableOnInstall(e.target.checked)}
            className="h-3.5 w-3.5"
          />
          安装后同时启用到全部支持 agent
        </label>
      </div>

      {updateReport && (
        <Card className="p-4">
          <div data-testid="skills-updates-report" className="mb-2 text-sm font-medium text-slate-700">
            更新检查完成 · {updateReport.updates.length} 个可更新 · {updateReport.failures.length} 个来源失败
          </div>
          {updateReport.updates.length === 0 && updateReport.failures.length === 0 && (
            <div className="text-xs text-slate-400">全部 skill 均为最新。</div>
          )}
          <ul className="space-y-1 text-xs">
            {updateReport.updates.map((u) => (
              <li key={u.skillId} data-testid={`skill-update-candidate-${u.skillId}`} className="flex items-center gap-2 text-slate-600">
                <span className="font-medium text-slate-800">{u.directory}</span>
                <span className="text-slate-400">{u.missing ? '本地缺失（需重装）' : '有新版本'}</span>
                <span className="text-slate-400">{u.repo}</span>
              </li>
            ))}
            {updateReport.failures.map((f) => (
              <li key={f.repo} className="text-amber-600">
                {f.repo}：{f.reason}
              </li>
            ))}
          </ul>
        </Card>
      )}

      <Card>
        <div className="flex items-center gap-2">
          <input
            data-testid="skill-search-input"
            data-search-input
            className="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-800 shadow-sm transition placeholder:text-slate-400 hover:border-slate-400 focus:border-brand-500 focus:outline-none focus:ring-4 focus:ring-brand-500/10"
            value={q}
            placeholder="在 skills.sh 搜索技能，例如：find-skills、commit、review…（按 / 聚焦）"
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void doSearch();
            }}
          />
          <Btn kind="primary" testId="skill-search-btn" disabled={searching || q.trim() === ''} onClick={() => void doSearch()}>
            {searching ? <Spinner className="h-4 w-4" /> : '搜索'}
          </Btn>
        </div>

        <div className="mt-3 flex items-center justify-between gap-2 border-t border-slate-100 pt-3">
          <Segmented
            value={tab}
            onChange={setTab}
            options={[
              {
                value: 'installed',
                testId: 'skills-tab-installed',
                label: (
                  <>
                    已安装{installed.length > 0 && <span className="ml-1 tabular-nums">{installed.length}</span>}
                  </>
                ),
              },
              {
                value: 'search',
                testId: 'skills-tab-search',
                disabled: searchResult === null,
                label: (
                  <>
                    搜索结果{results.length > 0 && <span className="ml-1 tabular-nums">{results.length}</span>}
                  </>
                ),
              },
            ]}
          />
          {tab === 'installed' && installedTotalPages > 1 && (
            <Pager page={safeInstalledPage} totalPages={installedTotalPages} onPage={setInstalledPage} />
          )}
          {tab === 'search' && searchResult !== null && (
            <Btn small onClick={clearSearch}>
              清空搜索
            </Btn>
          )}
        </div>

        <div className="mt-3">
          {tab === 'installed' ? (
            installed.length === 0 ? (
              <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-slate-300 bg-slate-50/50 py-10 text-center">
                <Icon name="skills" className="h-8 w-8 text-slate-300" />
                <p className="text-sm text-slate-400">还没有安装任何 skill——用上面的搜索或「安装本地 ZIP」开始，SKILL.md 是唯一的准入条件。</p>
              </div>
            ) : (
              <div className="grid gap-2.5 lg:grid-cols-2">
                {pageInstalled.map((s) => (
                  <InstalledSkillCard
                    key={s.id}
                    s={s}
                    togglePending={toggle.isPending}
                    toggleAllPending={toggleAll.isPending}
                    updatePending={update.isPending}
                    onToggle={(agentType, enabled) => toggle.mutate({ id: s.id, agentType, enabled })}
                    onToggleAll={(enabled) =>
                      toggleAll.mutate({
                        id: s.id,
                        enabled,
                        agentTypes: s.targets.filter((t) => t.enabled !== enabled).map((t) => t.agentType),
                      })
                    }
                    onUpdate={() => update.mutate(s.id)}
                    onRemove={() => setRemoving(s)}
                  />
                ))}
              </div>
            )
          ) : searching && results.length === 0 ? (
            <div className="flex items-center justify-center gap-2 py-8 text-sm text-slate-400">
              <Spinner className="h-4 w-4 text-brand-600" /> 正在搜索 skills.sh…
            </div>
          ) : results.length === 0 ? (
            <div className="py-8 text-center text-xs text-slate-400" data-testid="skill-search-empty">
              {searchResult === null ? '输入关键词搜索 skills.sh 收录的技能。' : '没有匹配结果（source 需为合法 GitHub 仓库坐标）'}
            </div>
          ) : (
            <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
              {results.map((s) => {
                const m = matchInstalled(s, installed);
                return (
                  <SearchResultCard
                    key={`${s.repo}#${s.skillId}`}
                    s={s}
                    exactInstalled={m.exact !== undefined}
                    repoInstalledCount={m.repoCount}
                    firstOfRepo={firstRowOfRepo.get(s.repo) === s.skillId}
                    collectionPending={installCollection.isPending && pendingRepo === s.repo}
                    installPending={install.isPending}
                    onInstall={() => install.mutate({ repo: s.repo, skill: s.skillId })}
                    onInstallCollection={() => {
                      setPendingRepo(s.repo);
                      installCollection.mutate(s.repo);
                    }}
                  />
                );
              })}
            </div>
          )}
        </div>
      </Card>

      {removing && (
        <ConfirmDialog
          title="卸载 Skill"
          message={`将备份并删除「${removing.name}」的 SSOT 目录与全部投影（用户改过的目录会保留）。确认卸载？`}
          confirmText="卸载"
          onCancel={() => setRemoving(null)}
          onConfirm={() => uninstall.mutate(removing.id)}
        />
      )}
    </div>
  );
}

/** 已安装 skill 卡片：元信息 + 分发 agent 芯片（点芯片即切换启用，支持全选/清空） + 操作 */
function InstalledSkillCard(props: {
  s: SkillWithStatus;
  togglePending: boolean;
  toggleAllPending: boolean;
  updatePending: boolean;
  onToggle: (agentType: string, enabled: boolean) => void;
  onToggleAll: (enabled: boolean) => void;
  onUpdate: () => void;
  onRemove: () => void;
}) {
  const { s } = props;
  const enabledCount = s.targets.filter((t) => t.enabled).length;
  return (
    <div
      data-testid={`skill-card-${s.id}`}
      className="flex flex-col rounded-xl border border-slate-200 bg-white p-4 transition-all duration-200 hover:-translate-y-0.5 hover:border-slate-300 hover:shadow-card-hover"
    >
      <div className="flex items-center gap-2">
        <span className="min-w-0 truncate text-sm font-semibold text-slate-800" title={s.name}>{s.name}</span>
        <Badge tone={s.source === 'local' ? 'gray' : 'blue'}>{sourceLabel(s.source)}</Badge>
        {s.installs !== undefined && <Badge tone="gray">{s.installs} 安装</Badge>}
      </div>
      {s.description && <div className="mt-1 line-clamp-2 text-xs leading-relaxed text-slate-500">{s.description}</div>}
      <div className="mt-1 truncate font-mono text-xs text-slate-400" title={s.repoOwner ? `${s.repoOwner}/${s.repoName}@${s.branch}` : s.directory}>
        {s.repoOwner ? `${s.repoOwner}/${s.repoName}@${s.branch}` : s.directory} · #{s.contentHash.slice(0, 8)}
      </div>

      <div className="mt-3" data-testid={`skill-targets-${s.id}`}>
        <div className="mb-1.5 flex items-center justify-between">
          <span className="text-xs font-medium uppercase tracking-wide text-slate-400">分发到 Agent</span>
          <span className="flex items-center gap-2">
            <button
              type="button"
              data-testid={`skill-all-${s.id}`}
              disabled={props.toggleAllPending || enabledCount === s.targets.length}
              title="分发到全部支持 agent"
              onClick={() => props.onToggleAll(true)}
              className="text-xs text-slate-400 transition-colors hover:text-brand-600 disabled:pointer-events-none disabled:opacity-40"
            >
              全选
            </button>
            <button
              type="button"
              data-testid={`skill-none-${s.id}`}
              disabled={props.toggleAllPending || enabledCount === 0}
              title="停用全部分发（我们自己的投影会删除，用户改过的目录保留）"
              onClick={() => props.onToggleAll(false)}
              className="text-xs text-slate-400 transition-colors hover:text-slate-600 disabled:pointer-events-none disabled:opacity-40"
            >
              清空
            </button>
            <span className="text-xs tabular-nums text-slate-400">{enabledCount}/{s.targets.length}</span>
          </span>
        </div>
        <div className="flex flex-wrap gap-1.5">
          {s.targets.map((t) => {
            const label = SKILL_AGENT_LABEL[t.agentType] ?? t.agentType;
            const tip = t.modified
              ? `${label}：目标目录被修改过（保留中，未受我们管理）`
              : !t.enabled && t.projected
                ? `${label}：目录仍在（未启用）`
                : `${label}：${t.enabled ? '已启用，点击停用' : '未启用，点击启用'}`;
            return (
              <button
                key={t.agentType}
                data-testid={`skill-toggle-${s.id}-${t.agentType}`}
                disabled={props.togglePending}
                title={tip}
                onClick={() => props.onToggle(t.agentType, !t.enabled)}
                className={`inline-flex h-7 items-center gap-1 rounded-full border px-2.5 text-xs font-medium transition-all duration-150 active:scale-[0.97] disabled:pointer-events-none disabled:opacity-50 ${
                  t.enabled
                    ? 'border-brand-200 bg-brand-50 text-brand-700'
                    : 'border-slate-200 bg-white text-slate-500 hover:border-slate-300 hover:text-slate-700'
                }`}
              >
                {t.enabled ? (
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" className="h-3 w-3" aria-hidden>
                    <path d="M20 6L9 17l-5-5" />
                  </svg>
                ) : (
                  <span className="h-1.5 w-1.5 rounded-full bg-slate-300" aria-hidden />
                )}
                <span className="max-w-28 truncate">{label}</span>
                {t.modified && <span className="h-1.5 w-1.5 rounded-full bg-amber-400" title="目标目录被修改过" aria-label="已修改" />}
                {!t.enabled && t.projected && <span className="h-1.5 w-1.5 rounded-full bg-emerald-300" title="目录仍在（未启用）" aria-label="目录仍在" />}
              </button>
            );
          })}
        </div>
      </div>

      <div className="mt-3 flex items-center gap-2 border-t border-slate-100 pt-3">
        {s.repoOwner && (
          <Btn small testId={`skill-update-${s.id}`} disabled={props.updatePending} onClick={props.onUpdate}>
            更新
          </Btn>
        )}
        <Btn small kind="danger" testId={`skill-uninstall-${s.id}`} className="ml-auto" onClick={props.onRemove}>
          卸载
        </Btn>
      </div>
    </div>
  );
}

/** 搜索结果卡片：已安装状态徽章 + 安装 / 整仓安装 */
function SearchResultCard(props: {
  s: { skillId: string; name: string; installs: number; repo: string; branch: string };
  exactInstalled: boolean;
  repoInstalledCount: number;
  firstOfRepo: boolean;
  collectionPending: boolean;
  installPending: boolean;
  onInstall: () => void;
  onInstallCollection: () => void;
}) {
  const { s } = props;
  return (
    <div
      data-testid={`skill-result-${s.skillId}`}
      className={`flex flex-col rounded-xl border p-4 transition-all duration-200 ${
        props.exactInstalled
          ? 'border-emerald-200 bg-emerald-50/40 hover:border-emerald-300'
          : 'border-slate-200 bg-white hover:-translate-y-0.5 hover:border-slate-300 hover:shadow-card-hover'
      }`}
    >
      <div className="flex items-center gap-2">
        <span className="min-w-0 truncate text-sm font-semibold text-slate-800" title={s.name}>{s.name}</span>
        {props.exactInstalled ? (
          <Badge tone="green">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" className="h-3 w-3" aria-hidden>
              <path d="M20 6L9 17l-5-5" />
            </svg>
            已安装
          </Badge>
        ) : props.repoInstalledCount > 0 ? (
          <Badge tone="blue">同仓库已装 {props.repoInstalledCount} 个</Badge>
        ) : null}
      </div>
      <div className="mt-0.5 truncate font-mono text-xs text-slate-400" title={`${s.repo}@${s.branch}`}>
        {s.repo}@{s.branch}
      </div>
      <div className="mt-2 flex items-center gap-1.5 text-xs text-slate-500">
        <span aria-hidden>🔥</span>
        <span className="tabular-nums">{s.installs.toLocaleString('en-US')}</span>
        <span>次安装</span>
      </div>
      <div className="mt-auto flex items-center gap-2 border-t border-slate-100 pt-3">
        {props.firstOfRepo && (
          <Btn
            small
            testId={`skill-install-collection-${s.repo}`}
            disabled={props.collectionPending || props.installPending}
            title={`一次下载 ${s.repo} 归档，安装其中全部技能`}
            onClick={props.onInstallCollection}
          >
            {props.collectionPending ? <Spinner className="h-3.5 w-3.5" /> : '安装整仓'}
          </Btn>
        )}
        <Btn
          small
          kind={props.exactInstalled ? 'default' : 'primary'}
          testId={`skill-install-${s.skillId}`}
          disabled={props.installPending}
          title={props.exactInstalled ? '重新下载并刷新本地副本' : undefined}
          className="ml-auto"
          onClick={props.onInstall}
        >
          {props.exactInstalled ? '重装' : '安装'}
        </Btn>
      </div>
    </div>
  );
}

function sourceLabel(source: string): string {
  if (source === 'github') return 'GitHub';
  if (source === 'skills.sh') return 'skills.sh';
  if (source === 'local') return '本地 ZIP';
  return source;
}
