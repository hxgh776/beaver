import { useState } from 'react';
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import type { RegistrySearchResult, RegistrySuggestion, ServerDefDTO } from '../api';
import { Badge, Btn, Card, Icon, Pagination, Segmented, Spinner, inputCls, useToast } from '../ui';
import type { TabKey } from '../App';

// 每页 12 张（3 列 × 4 行）：整页配合头部控制在了一屏内，翻页在卡片底部
const PAGE_SIZE = 12;

/** 搜索/浏览的数据源，按国内/国际分组；npm 仅在有关键词时可用（npm 没有可浏览目录） */
const SOURCE_GROUPS = [
  {
    region: '国内',
    sources: [
      { key: 'modelscope', label: '魔搭 ModelScope', title: '魔搭 MCP 广场（阿里）：1.2万+，中文描述 + 完整安装配置' },
      { key: 'baidu', label: '百度 MCP World', title: '百度 MCP World：6万+ 收录，仅浏览发现，不支持自动添加' },
    ],
  },
  {
    region: '国际',
    sources: [
      { key: 'official', label: '官方 Registry', title: '官方 MCP Registry：规范源头，约 1.7 万条' },
      { key: 'github', label: 'GitHub', title: 'GitHub MCP Registry：官方同构镜像，附完整 README' },
      { key: 'smithery', label: 'Smithery', title: 'Smithery：大而全的第三方注册表；连接托管端点需自备 API key' },
      { key: 'cline', label: 'Cline 市场', title: 'Cline 市场：人工精选约 150 个，含安装模板' },
      { key: 'docker', label: 'Docker 目录', title: 'Docker MCP 目录：安全审核约 270 个，需本机安装 Docker' },
      { key: 'mcpm', label: 'mcpm', title: 'mcpm.sh 聚合注册表：约 390 个 npm/uvx 安装模板' },
      { key: 'npm', label: 'npm', title: 'npm 搜索：需关键词，按周下载量排序；上游不可达时的兜底' },
    ],
  },
] as const;

type SourceKey = (typeof SOURCE_GROUPS)[number]['sources'][number]['key'];

const SOURCE_KEYS = SOURCE_GROUPS.flatMap((g) => g.sources.map((s) => s.key)) as readonly SourceKey[];

/** 结果实际来源 → 展示名（回退时可能与所选源不同） */
const SOURCE_LABEL: Record<SourceKey, string> = Object.fromEntries(
  SOURCE_GROUPS.flatMap((g) => g.sources.map((s) => [s.key, s.label])),
) as Record<SourceKey, string>;

/** 结果实际来源 → 结果区副标题（安装注意事项） */
const SOURCE_NOTE: Record<SourceKey, string> = {
  official: '来源：官方 MCP Registry',
  github: '来源：GitHub MCP Registry（官方同构镜像）',
  smithery: '来源：Smithery Registry——添加后需在编辑中配置 Smithery API key（Authorization 头）',
  npm: '来源：npm 搜索回退（上游 registry 不可达），npx 型、默认网关模式',
  modelscope: '来源：魔搭 ModelScope MCP 广场——中文描述，优先本地 stdio 配置；托管端点可能需 ModelScope token',
  baidu: '来源：百度 MCP World——仅浏览发现（无安装配置），可按名称到其他源搜索后添加',
  cline: '来源：Cline 市场（人工精选）——从官方安装模板解析出可执行配置',
  docker: '来源：Docker MCP 目录（安全审核）——需本机安装 Docker，以 docker run 运行',
  mcpm: '来源：mcpm.sh 聚合注册表——npm/uvx 安装模板',
};

/** 热度数字格式化：≥1万 → x.x万，其余千分位 */
function formatCount(n: number): string {
  if (n >= 10000) return `${(n / 10000).toFixed(1).replace(/\.0$/, '')}万`;
  return n.toLocaleString('en-US');
}

/** 热度徽章文案：npm=周下载量，Smithery/mcpm=使用次数，魔搭=调用量，百度=star */
function heatLabel(r: RegistrySuggestion): string | null {
  if (r.useCount === undefined) return null;
  if (r.source === 'npm') return `周下载 ${formatCount(r.useCount)}`;
  if (r.source === 'modelscope') return `${formatCount(r.useCount)} 次调用`;
  if (r.source === 'baidu') return `⭐ ${formatCount(r.useCount)}`;
  return `${formatCount(r.useCount)} 次使用`;
}

export function DiscoveryPage({ onNavigate }: { onNavigate?: (tab: TabKey) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [input, setInput] = useState('');
  const [query, setQuery] = useState('');
  const [source, setSource] = useState<SourceKey>('official');
  const [page, setPage] = useState(0);

  // 已收录的 server：搜索结果据此标注「已添加」，与 MCP 管理页共享缓存
  const servers = useQuery({
    queryKey: ['servers'],
    queryFn: () => api.get<{ servers: ServerDefDTO[] }>('/api/servers'),
  });

  // 进入页面即浏览官方 registry 收录的 MCP；输入关键词后回车/点搜索切换为搜索模式
  const results = useQuery({
    queryKey: ['registry', query, source, page],
    queryFn: () =>
      api.get<RegistrySearchResult>(
        `/api/registry/search?q=${encodeURIComponent(query)}&limit=${PAGE_SIZE}&offset=${page * PAGE_SIZE}&source=${source}`,
      ),
    placeholderData: keepPreviousData,
    retry: false,
  });

  const search = (q: string): void => {
    setQuery(q);
    setPage(0);
  };

  const switchSource = (s: SourceKey): void => {
    setSource(s);
    setPage(0);
  };

  const add = useMutation({
    mutationFn: (s: RegistrySuggestion) => api.post<{ server: { id: string } }>('/api/servers', s.suggestion),
    onSuccess: (res) => {
      toast.ok(`已添加 ${res.server.id}——在「MCP 管理」可编辑，在「Agent 分发」可启用到 agent`);
      void qc.invalidateQueries();
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const list = results.data?.servers ?? [];
  const served = results.data?.source;
  const browsAll = query === '';
  const addedIds = new Set((servers.data?.servers ?? []).map((s) => s.id));

  return (
    <div>
      <Card title="MCP 发现" subtitle="国内外九源聚合：魔搭 / 百度 / 官方 / GitHub / Smithery / Cline / Docker / mcpm / npm。搜索结果按热度排序（npm=周下载量、魔搭=调用量等）；官方源不可达时自动回退 npm。">
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            search(input.trim());
          }}
        >
          <input
            data-testid="registry-query"
            data-search-input
            className={`${inputCls} flex-1`}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="搜索 MCP server，例如：context7、filesystem、高德、notion…（留空 = 浏览全部；按 / 聚焦）"
          />
          <Btn kind="primary" disabled={results.isFetching} onClick={() => search(input.trim())}>
            {results.isFetching ? '查询中…' : '搜索'}
          </Btn>
        </form>
        <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2">
          {SOURCE_GROUPS.map((g) => (
            <div key={g.region} className="flex items-center gap-2">
              <span className="text-xs text-slate-400">{g.region}</span>
              <Segmented
                value={source}
                onChange={switchSource}
                options={g.sources.map((s) => ({
                  value: s.key,
                  testId: `source-${s.key}`,
                  disabled: browsAll && s.key === 'npm',
                  title: browsAll && s.key === 'npm' ? 'npm 源需要搜索关键词' : s.title,
                  label: s.label,
                }))}
              />
            </div>
          ))}
        </div>
      </Card>

      <Card
        title={browsAll ? '浏览全部 MCP' : `搜索“${query}”`}
        subtitle={served !== undefined ? SOURCE_NOTE[served] : undefined}
        actions={
          browsAll ? undefined : (
            <Btn small onClick={() => { setInput(''); search(''); }}>
              清空搜索
            </Btn>
          )
        }
      >
        {results.isPending ? (
          <div className="flex items-center justify-center gap-2 py-8 text-sm text-slate-400">
            <Spinner className="h-4 w-4 text-brand-600" />
            正在加载 MCP 列表…
          </div>
        ) : results.isError ? (
          <p className="py-4 text-sm text-amber-600">{String((results.error as Error).message)}</p>
        ) : list.length === 0 ? (
          <p className="py-8 text-center text-sm text-slate-400">
            {browsAll ? '没有收录的 server。' : '没有匹配的 server——可换一个数据源试试。'}
          </p>
        ) : (
          <>
            <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
              {list.map((r) => {
                const isAdded = addedIds.has(r.suggestion?.name ?? r.name);
                return (
                  <div
                    key={`${r.source ?? served}-${r.name}`}
                    data-testid={`registry-${r.name}`}
                    className={`flex flex-col rounded-xl border p-4 transition-all duration-200 ${
                      isAdded
                        ? 'border-emerald-200 bg-emerald-50/40 hover:border-emerald-300'
                        : 'border-slate-200 bg-white hover:-translate-y-0.5 hover:border-slate-300 hover:shadow-card-hover'
                    }`}
                  >
                    <div className="flex items-start gap-2.5">
                      <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-gradient-to-br from-brand-50 to-brand-100 text-brand-600 ring-1 ring-brand-200/70">
                        <Icon name="servers" className="h-5 w-5" />
                      </div>
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-1.5">
                          <span className="min-w-0 truncate text-sm font-semibold text-slate-800" title={r.name}>{r.name}</span>
                          {isAdded && (
                            <Badge tone="green">
                              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" className="h-3 w-3" aria-hidden>
                                <path d="M20 6L9 17l-5-5" />
                              </svg>
                              已添加
                            </Badge>
                          )}
                        </div>
                        <div className="mt-0.5 flex flex-wrap items-center gap-1">
                          {r.suggestion === null ? (
                            <Badge tone="gray">不支持自动添加</Badge>
                          ) : (
                            <>
                              <Badge tone={r.source === 'smithery' ? 'amber' : r.source === 'npm' ? 'gray' : 'blue'}>
                                {SOURCE_LABEL[r.source ?? served ?? 'official'] ?? r.source}
                              </Badge>
                              <Badge tone="blue">{r.suggestion.transport}</Badge>
                              {heatLabel(r) !== null && <Badge tone="gray">🔥 {heatLabel(r)}</Badge>}
                            </>
                          )}
                        </div>
                      </div>
                    </div>
                    <p className="mt-2 line-clamp-2 text-xs leading-relaxed text-slate-500" title={r.description}>
                      {r.description}
                    </p>
                    <div className="mt-auto flex items-center gap-2 border-t border-slate-100 pt-2.5">
                      {r.suggestion !== null && (
                        <Badge tone="green">网关模式</Badge>
                      )}
                      <div className="ml-auto">
                        {r.suggestion !== null &&
                          (isAdded ? (
                            <Btn small onClick={() => onNavigate?.('sync')}>
                              去分发
                            </Btn>
                          ) : (
                            <Btn small kind="primary" disabled={add.isPending} onClick={() => add.mutate(r)}>
                              添加
                            </Btn>
                          ))}
                      </div>
                    </div>
                  </div>
                );
              })}
            </div>
            <Pagination
              page={page}
              hasMore={results.data?.hasMore === true}
              total={results.data?.total}
              totalPages={results.data?.totalPages}
              pageSize={PAGE_SIZE}
              onPage={setPage}
            />
          </>
        )}
      </Card>
    </div>
  );
}
