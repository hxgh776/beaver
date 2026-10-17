import { useState } from 'react';
import type { ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import type { AgentInfo, ImportGroup, RuntimeTool, StatusInfo } from '../api';
import { Badge, Btn, Card, Pager, Segmented, rowCls, theadCls, useToast } from '../ui';
import type { TabKey } from '../App';

/** 每页 agent 卡片数：4 列 × 2 行，配卡片头部的数字分页切换 */
const AGENT_PAGE_SIZE = 8;

/** 探测结果徽章：候选配置文件存在 →「已安装」；否则「未发现」 */
function DetectBadge({ agent }: { agent: AgentInfo }) {
  if (agent.detected) {
    return (
      <span title={`检测到配置文件：${agent.configPaths[0]}`}>
        <Badge tone="green">已安装</Badge>
      </span>
    );
  }
  return (
    <span title="未发现配置文件">
      <Badge tone="gray">未发现</Badge>
    </span>
  );
}

export function StatusPage({ onNavigate }: { onNavigate?: (tab: TabKey) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [tab, setTab] = useState<'agents' | 'import'>('agents');
  const [page, setPage] = useState(0);

  const status = useQuery({ queryKey: ['status'], queryFn: () => api.get<StatusInfo>('/api/status') });
  const agents = useQuery({
    queryKey: ['agents'],
    queryFn: () => api.get<{ agents: AgentInfo[] }>('/api/agents?refresh=1'),
  });
  const imports = useQuery({
    queryKey: ['import-preview'],
    queryFn: () => api.get<{ groups: ImportGroup[] }>('/api/import/preview'),
  });
  const environment = useQuery({
    queryKey: ['environment'],
    queryFn: () => api.get<{ tools: RuntimeTool[] }>('/api/environment'),
  });

  const doImport = useMutation({
    mutationFn: (fingerprints: string[]) => api.post<{ added: unknown[] }>('/api/import', { fingerprints }),
    onSuccess: (res) => {
      toast.ok(`已导入 ${res.added.length} 个 server，去「Agent 分发」页勾选同步`);
      void qc.invalidateQueries();
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const rerunDetect = useMutation({
    mutationFn: () => agents.refetch(),
    onSuccess: (res) => {
      const list = res.data?.agents ?? [];
      const installed = list.filter((a) => a.detected).length;
      toast.ok(`探测完成：${installed} 个已安装，${list.length - installed} 个未发现`);
    },
    onError: (e) => toast.err(`探测失败：${String((e as Error).message)}`),
  });

  const pending = (imports.data?.groups ?? []).filter((g) => !g.alreadyImported);
  const agentList = agents.data?.agents ?? [];
  const detectedCount = agentList.filter((a) => a.detected).length;
  // 列表缩短（如重探后）时收敛到合法页码
  const totalPages = Math.max(1, Math.ceil(agentList.length / AGENT_PAGE_SIZE));
  const safePage = Math.min(page, totalPages - 1);
  const pageAgents = agentList.slice(safePage * AGENT_PAGE_SIZE, (safePage + 1) * AGENT_PAGE_SIZE);

  return (
    <div>
      <Card title="运行状态">
        <dl className="grid grid-cols-2 gap-x-6 gap-y-3 text-sm md:grid-cols-3 xl:grid-cols-5">
          <div className="min-w-0 md:col-span-2 xl:col-span-1">
            <dt className="text-xs text-slate-400">数据目录</dt>
            <dd data-testid="home-dir" className="mt-1">
              <span className="block truncate font-mono text-xs leading-relaxed text-slate-600" title={status.data?.homeDir}>
                {status.data?.homeDir}
              </span>
            </dd>
          </div>
          <Stat label="运行端口">{status.data?.port}</Stat>
          <Stat label="已运行">
            {Math.floor((status.data?.uptimeSec ?? 0) / 60)} 分 {((status.data?.uptimeSec ?? 0) % 60)} 秒
          </Stat>
          <Stat label="Server / 分组 / 直连绑定">
            {status.data?.serverCount} / {status.data?.groupCount ?? 0} / {status.data?.bindingCount}
          </Stat>
          <Stat label="已检测到 Agent">
            {detectedCount} / {agentList.length}
          </Stat>
        </dl>
        <div className="mt-3 flex flex-wrap gap-1.5 border-t border-slate-100 pt-3">
          {(environment.data?.tools ?? []).map((t) => (
            <span
              key={t.name}
              className="inline-flex items-center gap-1.5 rounded-full border border-slate-200 bg-white px-2 py-0.5 text-xs shadow-sm"
              title={`${t.role}${t.version !== undefined ? ` · ${t.version}` : ''}`}
            >
              <span className={`h-1.5 w-1.5 rounded-full ${t.found ? 'bg-emerald-500' : 'bg-rose-500'}`} />
              <span className="font-medium text-slate-700">{t.name}</span>
              <span className="text-slate-400">{t.found ? t.role : `${t.role} · 缺失`}</span>
            </span>
          ))}
        </div>
      </Card>

      <Card
        title="本机 Coding Agent"
        subtitle="存在任一候选配置即视为已安装；点 agent 卡片可到「Agent 分发」页配置"
        actions={
          <Btn
            small
            disabled={agents.isFetching}
            onClick={() => void rerunDetect.mutate()}
            testId="redetect"
          >
            {agents.isFetching ? '探测中…' : '重新探测'}
          </Btn>
        }
      >
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <Segmented
            value={tab}
            onChange={setTab}
            options={[
              {
                value: 'agents',
                testId: 'overview-tab-agents',
                label: (
                  <>
                    本机 Agent{agentList.length > 0 && <span className="ml-1 tabular-nums">{agentList.length}</span>}
                  </>
                ),
              },
              {
                value: 'import',
                testId: 'overview-tab-import',
                title: '扫描各 agent 已有的 MCP 配置，一键收编进注册表',
                label: (
                  <>
                    从配置导入{pending.length > 0 && <span className="ml-1 tabular-nums text-brand-600">{pending.length}</span>}
                  </>
                ),
              },
            ]}
          />
          {tab === 'agents' && (
            <Pager
              page={safePage}
              totalPages={totalPages}
              onPage={setPage}
              className={agents.isFetching ? 'opacity-50 transition-opacity' : 'transition-opacity'}
            />
          )}
        </div>

        {tab === 'agents' ? (
          agents.isFetching && !agents.data ? (
            <p data-testid="detect-progress" className="text-sm text-slate-400">
              正在扫描本机 agent 配置文件…
            </p>
          ) : (
            <div className="grid grid-cols-2 gap-2.5 md:grid-cols-4">
              {pageAgents.map((a) => (
                <button
                  key={a.agentType}
                  data-testid={`agent-card-${a.agentType}`}
                  onClick={() => onNavigate?.('sync')}
                  className="group flex min-w-0 flex-col rounded-xl border border-slate-200 bg-white px-3 py-2.5 text-left transition-all duration-200 hover:-translate-y-0.5 hover:border-brand-300 hover:shadow-card-hover"
                >
                  <div className="flex items-center justify-between gap-1">
                    <span className="truncate text-sm font-semibold text-slate-800" title={a.displayName}>
                      {a.displayName}
                    </span>
                    <DetectBadge agent={a} />
                  </div>
                  <div className="mt-0.5 truncate font-mono text-xs text-slate-400" title={a.configPaths[0]}>
                    {a.configPaths[0]}
                  </div>
                  <div className="mt-1 text-xs text-slate-500">
                    分发 <span className="font-semibold tabular-nums text-slate-700">{a.boundServerIds.length}</span> 个 MCP
                    （含分组）
                  </div>
                </button>
              ))}
            </div>
          )
        ) : pending.length === 0 ? (
          <p className="text-sm text-slate-500">没有发现可导入的新 server 配置。</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className={theadCls}>
                <th className="py-2">名称</th>
                <th className="py-2">传输</th>
                <th className="py-2">来源</th>
                <th className="py-2"></th>
              </tr>
            </thead>
            <tbody>
              {pending.map((g) => (
                <tr key={g.fingerprint} className={rowCls}>
                  <td className="py-2 font-medium text-slate-800">{g.suggested.name}</td>
                  <td className="py-2">{g.suggested.transport}</td>
                  <td className="py-2 text-xs text-slate-500">
                    {g.candidates.map((c) => c.agentType).join(', ')}
                  </td>
                  <td className="py-2 text-right">
                    <Btn
                      small
                      kind="primary"
                      testId={`import-${g.suggested.name}`}
                      onClick={() => doImport.mutate([g.fingerprint])}
                    >
                      导入
                    </Btn>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {tab === 'import' && pending.length > 1 && (
          <div className="mt-3 flex justify-end border-t border-slate-100 pt-2.5">
            <Btn small kind="primary" disabled={doImport.isPending} onClick={() => doImport.mutate(pending.map((g) => g.fingerprint))}>
              全部导入（{pending.length}）
            </Btn>
          </div>
        )}
      </Card>
    </div>
  );
}

function Stat({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-slate-400">{label}</dt>
      <dd className="mt-1 truncate text-sm font-semibold tabular-nums text-slate-900">{children}</dd>
    </div>
  );
}
