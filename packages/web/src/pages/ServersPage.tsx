import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import type { GatewayInfo, RuntimeTool, ServerDefDTO, ToolInfo } from '../api';
import { Badge, Btn, Card, ConfirmDialog, CopyBtn, ErrorBanner, Field, Icon, Modal, Pager, Spinner, Toggle, inputCls, useToast } from '../ui';
import type { TabKey } from '../App';

/** 每页 server 卡片数：3 列 × 2 行，配卡片头部的数字分页切换 */
const SERVER_PAGE_SIZE = 6;

/** 新增弹窗的内置样例：常见 MCP server 的推荐启动配置（选中即填充表单，可再手改） */
interface ServerTemplate {
  key: string;
  name: string;
  desc: string;
  transport: 'stdio' | 'http';
  command?: string;
  args?: string;
  url?: string;
  /** 套用后提醒用户还需补充的内容 */
  note?: string;
}

const TEMPLATES: ServerTemplate[] = [
  { key: 'context7', name: 'Context7', desc: '查库/框架的最新版文档', transport: 'stdio', command: 'npx', args: '-y @upstash/context7-mcp' },
  { key: 'filesystem', name: 'Filesystem', desc: '读写指定目录的本地文件', transport: 'stdio', command: 'npx', args: '-y @modelcontextprotocol/server-filesystem <你的目录>', note: '把最后一个参数 <你的目录> 改成实际路径' },
  { key: 'fetch', name: 'Fetch', desc: '抓取网页并转为 markdown', transport: 'stdio', command: 'uvx', args: 'mcp-server-fetch' },
  { key: 'memory', name: 'Memory', desc: '跨会话长期记忆（知识图谱）', transport: 'stdio', command: 'npx', args: '-y @modelcontextprotocol/server-memory' },
  { key: 'sequential-thinking', name: 'Sequential Thinking', desc: '分步推理与任务规划', transport: 'stdio', command: 'npx', args: '-y @modelcontextprotocol/server-sequential-thinking' },
  { key: 'git', name: 'Git', desc: '读取 / 检索本地 Git 仓库', transport: 'stdio', command: 'uvx', args: 'mcp-server-git' },
  { key: 'everything', name: 'Everything', desc: '官方演示 server，验证链路用', transport: 'stdio', command: 'npx', args: '-y @modelcontextprotocol/server-everything' },
  { key: 'deepwiki', name: 'DeepWiki', desc: '开源仓库结构化问答（远程）', transport: 'http', url: 'https://mcp.deepwiki.com/mcp' },
];

/** 按空格拆分参数；支持单/双引号包裹含空格的片段（如 "D:\my dir"），也兼容每行一个参数 */
function splitArgs(text: string): string[] {
  const out: string[] = [];
  for (const line of text.split(/\r?\n/)) {
    const re = /"([^"]*)"|'([^']*)'|(\S+)/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(line)) !== null) {
      const piece = m[1] ?? m[2] ?? m[3] ?? '';
      if (piece !== '') out.push(piece);
    }
  }
  return out;
}

/** 回填表单时把含空格的参数包上引号，保证 splitArgs 能还原 */
function quoteArg(a: string): string {
  return /\s/.test(a) ? `"${a}"` : a;
}

interface FormState {
  id?: string;
  name: string;
  transport: 'stdio' | 'http' | 'sse';
  command: string;
  args: string;
  env: string;
  url: string;
  gatewayMode: boolean;
  enabled: boolean;
  concurrency: string;
}

const EMPTY: FormState = {
  name: '',
  transport: 'stdio',
  command: '',
  args: '',
  env: '',
  url: '',
  gatewayMode: false,
  enabled: true,
  concurrency: '1',
};

/** stdio 命令首词 → 依赖的运行时（缺环境时给警告） */
const COMMAND_RUNTIME: Record<string, string> = {
  node: 'node',
  npx: 'npx',
  uvx: 'uvx',
  docker: 'docker',
};

function toForm(s: ServerDefDTO): FormState {
  return {
    id: s.id,
    name: s.name,
    transport: s.transport,
    command: s.command ?? '',
    args: (s.args ?? []).map(quoteArg).join(' '),
    env: Object.entries(s.env ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join('\n'),
    url: s.url ?? '',
    gatewayMode: s.gatewayMode,
    enabled: s.enabled,
    concurrency: String(s.concurrency ?? 1),
  };
}

export function ServersPage({ onNavigate }: { onNavigate?: (tab: TabKey) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [form, setForm] = useState<FormState | null>(null);
  const [toolsFor, setToolsFor] = useState<ServerDefDTO | null>(null);
  const [removing, setRemoving] = useState<ServerDefDTO | null>(null);
  const [filter, setFilter] = useState('');
  const [page, setPage] = useState(0);

  const servers = useQuery({
    queryKey: ['servers'],
    queryFn: () => api.get<{ servers: ServerDefDTO[] }>('/api/servers'),
  });
  // 运行态与配置态分开：「启用」只是开关，是否真的连得上看这里的快照
  const gateway = useQuery({
    queryKey: ['gateway'],
    queryFn: () => api.get<GatewayInfo>('/api/gateway'),
    refetchInterval: 5000,
  });
  const environment = useQuery({
    queryKey: ['environment'],
    queryFn: () => api.get<{ tools: RuntimeTool[] }>('/api/environment'),
  });
  const missingRuntimes = new Set(
    (environment.data?.tools ?? []).filter((t) => !t.found).map((t) => t.name),
  );

  const runtimeWarning = (s: ServerDefDTO): string | null => {
    if (s.transport !== 'stdio' || s.command === undefined) return null;
    const base = s.command.split(/[\\/]/).at(-1)?.toLowerCase() ?? s.command;
    const need = COMMAND_RUNTIME[base];
    if (need !== undefined && missingRuntimes.has(need)) return `缺少运行时 ${need}`;
    return null;
  };

  const save = useMutation({
    mutationFn: (f: FormState) => {
      const body: Record<string, unknown> = {
        ...(f.id !== undefined ? { id: f.id } : {}),
        name: f.name,
        transport: f.transport,
        gatewayMode: f.gatewayMode,
        enabled: f.enabled,
      };
      if (f.transport === 'stdio') {
        body['command'] = f.command;
        body['args'] = splitArgs(f.args);
        const env: Record<string, string> = {};
        for (const line of f.env.split('\n')) {
          const idx = line.indexOf('=');
          if (idx > 0) env[line.slice(0, idx).trim()] = line.slice(idx + 1);
        }
        if (Object.keys(env).length > 0) body['env'] = env;
        const concurrency = Number(f.concurrency);
        if (Number.isInteger(concurrency) && concurrency >= 1) body['concurrency'] = concurrency;
      } else {
        body['url'] = f.url;
      }
      return f.id !== undefined
        ? api.patch<{ server: ServerDefDTO }>(`/api/servers/${f.id}`, body)
        : api.post<{ server: ServerDefDTO }>('/api/servers', body);
    },
    onSuccess: () => {
      toast.ok('已保存');
      setForm(null);
      void qc.invalidateQueries();
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.del(`/api/servers/${id}`),
    onSuccess: (_res, id) => {
      setRemoving(null);
      toast.ok(`已删除 ${id}（agent 配置片段会在下次分发时清理）`);
      void qc.invalidateQueries();
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const quickToggle = useMutation({
    mutationFn: ({ id, body }: { id: string; body: Record<string, unknown> }) =>
      api.patch(`/api/servers/${id}`, body),
    onSuccess: () => void qc.invalidateQueries(),
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const list = servers.data?.servers ?? [];
  const kw = filter.trim().toLowerCase();
  /** 保存前置校验：名称必填；stdio 需要命令，http/sse 需要 URL */
  const canSave =
    form !== null &&
    form.name.trim() !== '' &&
    (form.transport === 'stdio' ? form.command.trim() !== '' : form.url.trim() !== '');
  const shown =
    kw === ''
      ? list
      : list.filter((s) => `${s.name} ${s.id} ${s.command ?? ''} ${(s.args ?? []).join(' ')} ${s.url ?? ''}`.toLowerCase().includes(kw));
  // 筛选/列表变化后收敛到合法页码
  const totalPages = Math.max(1, Math.ceil(shown.length / SERVER_PAGE_SIZE));
  const safePage = Math.min(page, totalPages - 1);
  const pageServers = shown.slice(safePage * SERVER_PAGE_SIZE, (safePage + 1) * SERVER_PAGE_SIZE);

  return (
    <div>
      <Card
        subtitle={kw === '' ? `共 ${list.length} 个 server；新增/编辑在弹窗中进行` : `筛选出 ${shown.length} / ${list.length} 个`}
        actions={
          <>
            <Pager page={safePage} totalPages={totalPages} onPage={setPage} />
            {list.length > 3 && (
              <input
                data-search-input
                className="h-7 w-40 rounded-md border border-slate-300 bg-white px-2.5 text-xs text-slate-800 shadow-sm transition placeholder:text-slate-400 hover:border-slate-400 focus:border-brand-500 focus:outline-none focus:ring-4 focus:ring-brand-500/10"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder="筛选名称 / 命令…"
              />
            )}
            <Btn small kind="primary" testId="btn-add-server" onClick={() => setForm({ ...EMPTY })}>
              添加 Server
            </Btn>
          </>
        }
      >
        {list.length === 0 ? (
          <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed border-slate-300 bg-slate-50/50 py-10 text-center">
            <Icon name="servers" className="h-8 w-8 text-slate-300" />
            <p className="text-sm text-slate-400">还没有 server——点「添加 Server」从内置样例一键开始，或到发现页收录 / 导入现有配置。</p>
            <div className="flex gap-2">
              <Btn small kind="primary" onClick={() => onNavigate?.('discovery')}>
                去「MCP 发现」
              </Btn>
              <Btn small onClick={() => onNavigate?.('status')}>
                去「概览」导入
              </Btn>
            </div>
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-2.5 md:grid-cols-2 xl:grid-cols-3">
            {pageServers.map((s) => {
              const warn = runtimeWarning(s);
              // 网关型 + 启用中才有运行态（直连模式进程由 agent 自己拉起，Beaver 不掌握）
              const gw = s.gatewayMode && s.enabled ? gateway.data?.upstreams.find((u) => u.id === s.id) : undefined;
              const gwFailed = gw !== undefined && (gw.status === 'error' || gw.status === 'backoff') && gw.lastError !== null;
              return (
                <div
                  key={s.id}
                  data-testid={`server-row-${s.id}`}
                  className="flex flex-col rounded-xl border border-slate-200 px-3.5 py-3 transition-all duration-200 hover:-translate-y-0.5 hover:border-slate-300 hover:shadow-card-hover"
                >
                  <div className="mb-1 flex items-start justify-between gap-2">
                    <div className="min-w-0">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-sm font-semibold text-slate-800" title={s.name}>{s.name}</span>
                        {s.gatewayMode ? <Badge tone="blue">网关</Badge> : <Badge tone="gray">直连</Badge>}
                      </div>
                      <div className="truncate text-xs text-slate-400" title={s.id}>{s.id}</div>
                    </div>
                    <div className="flex shrink-0 items-center gap-1.5">
                      {gw !== undefined && <RuntimeBadge status={gw.status} />}
                      {s.enabled ? <Badge tone="green">启用</Badge> : <Badge tone="red">停用</Badge>}
                    </div>
                  </div>
                  <div className="mb-2 flex items-center gap-1.5">
                    <span
                      className="min-w-0 flex-1 truncate font-mono text-xs text-slate-500"
                      title={s.transport === 'stdio' ? `${s.command ?? ''} ${(s.args ?? []).join(' ')}` : s.url}
                    >
                      {s.transport === 'stdio' ? `${s.command ?? ''} ${(s.args ?? []).join(' ')}` : s.url}
                    </span>
                    <CopyBtn
                      text={s.transport === 'stdio' ? `${s.command ?? ''} ${(s.args ?? []).join(' ')}` : (s.url ?? '')}
                      label=""
                      className="opacity-60 hover:opacity-100"
                    />
                  </div>
                  {warn !== null && (
                    <div className="mb-2">
                      <Badge tone="amber">⚠ {warn}</Badge>
                    </div>
                  )}
                  {gwFailed && (
                    <div className="mb-2 truncate text-xs text-rose-600" title={gw?.lastError ?? ''}>
                      {gw?.status === 'backoff' ? '连接断开，重试中：' : '连接失败：'}
                      {gw?.lastError}
                    </div>
                  )}
                  <div className="mt-auto flex gap-2 border-t border-slate-100 pt-2">
                    <Btn small onClick={() => setForm(toForm(s))}>编辑</Btn>
                    <Btn small onClick={() => setToolsFor(s)}>工具</Btn>
                    <Btn
                      small
                      onClick={() => void quickToggle.mutate({ id: s.id, body: { enabled: !s.enabled } })}
                    >
                      {s.enabled ? '停用' : '启用'}
                    </Btn>
                    <Btn small kind="danger" onClick={() => setRemoving(s)}>删除</Btn>
                  </div>
                </div>
              );
            })}
            {shown.length === 0 && (
              <p className="col-span-full py-6 text-center text-sm text-slate-400">
                没有匹配「{filter}」的 server。
              </p>
            )}
          </div>
        )}
      </Card>

      {form !== null && (
        <Modal
          title={form.id !== undefined ? `编辑：${form.name || form.id}` : '添加 Server'}
          onClose={() => setForm(null)}
          wide
          footer={
            <>
              <Btn onClick={() => setForm(null)}>取消</Btn>
              <Btn
                kind="primary"
                testId="form-save"
                disabled={save.isPending || !canSave}
                title={canSave ? undefined : '请先填写名称，以及启动入口（命令或 URL）'}
                onClick={() => save.mutate(form)}
              >
                {save.isPending ? '保存中…' : '保存'}
              </Btn>
            </>
          }
        >
          <ServerForm form={form} setForm={setForm} />
        </Modal>
      )}
      {toolsFor !== null && <ToolsModal server={toolsFor} onClose={() => setToolsFor(null)} />}
      {removing !== null && (
        <ConfirmDialog
          title="删除 Server"
          message={`确定删除「${removing.name}」（${removing.id}）？已分发到 agent 的配置片段会在下次分发时自动清理。`}
          onConfirm={() => remove.mutate(removing.id)}
          onCancel={() => setRemoving(null)}
        />
      )}
    </div>
  );
}

/** 运行态徽标：来自网关快照，与配置态「启用/停用」分开——启用 ≠ 连得上 */
function RuntimeBadge({ status }: { status: string }) {
  if (status === 'ready') return <Badge tone="green">运行中</Badge>;
  if (status === 'starting') return <Badge tone="blue">启动中</Badge>;
  if (status === 'backoff') return <Badge tone="amber">重连中</Badge>;
  if (status === 'error') return <Badge tone="red">出错</Badge>;
  return <Badge tone="gray">未运行</Badge>;
}

/** 新增时的样例区：点一张卡即填充下方表单；编辑已有 server 时不显示 */
function TemplateGrid({ appliedKey, onPick }: { appliedKey: string | null; onPick: (t: ServerTemplate) => void }) {
  const applied = TEMPLATES.find((t) => t.key === appliedKey);
  return (
    <div className="mb-4 rounded-xl border border-slate-200 bg-slate-50/60 p-3">
      <p className="mb-2 text-xs font-medium text-slate-500">从样例开始——点一张卡片自动填充下方表单，再按需微调：</p>
      <div className="grid grid-cols-2 gap-1.5 sm:grid-cols-4">
        {TEMPLATES.map((t) => (
          <button
            key={t.key}
            type="button"
            data-testid={`tpl-${t.key}`}
            title={t.desc}
            onClick={() => onPick(t)}
            className={`rounded-lg border px-2.5 py-1.5 text-left transition ${
              appliedKey === t.key
                ? 'border-brand-500 bg-brand-50 ring-1 ring-brand-500'
                : 'border-slate-200 bg-white hover:border-brand-400 hover:shadow-card-hover'
            }`}
          >
            <span className="flex items-center gap-1 text-xs font-semibold text-slate-700">
              <span className="truncate">{t.name}</span>
              {t.transport === 'http' && <Badge tone="blue">远程</Badge>}
            </span>
            <span className="mt-0.5 block truncate text-[11px] leading-tight text-slate-400">{t.desc}</span>
          </button>
        ))}
      </div>
      {applied?.note !== undefined && (
        <p className="mt-2 text-xs text-amber-600">已套用 {applied.name}：{applied.note}</p>
      )}
    </div>
  );
}

/** 分发模式的两个可选卡片：代替之前的单个开关——关=直连、开=网关的开关语义太难懂 */
function ModeCard(props: { selected: boolean; testId: string; title: string; desc: string; onClick: () => void }) {
  return (
    <button
      type="button"
      data-testid={props.testId}
      onClick={props.onClick}
      className={`rounded-xl border px-3 py-2.5 text-left transition ${
        props.selected
          ? 'border-brand-500 bg-brand-50 ring-1 ring-brand-500'
          : 'border-slate-200 bg-white hover:border-slate-300'
      }`}
    >
      <span className="flex items-center gap-1.5 text-sm font-semibold text-slate-800">
        {props.title}
        {props.selected && <Icon name="check" className="h-3.5 w-3.5 text-brand-600" />}
      </span>
      <span className="mt-1 block text-xs leading-relaxed text-slate-500">{props.desc}</span>
    </button>
  );
}

function ServerForm({ form, setForm }: { form: FormState; setForm: (f: FormState) => void }) {
  const f = form;
  const [appliedTpl, setAppliedTpl] = useState<string | null>(null);
  const set = (patch: Partial<FormState>): void => setForm({ ...f, ...patch });
  const applyTemplate = (t: ServerTemplate): void => {
    setAppliedTpl(t.key);
    setForm({
      ...f,
      name: t.name,
      transport: t.transport,
      command: t.command ?? '',
      args: t.args ?? '',
      url: t.url ?? '',
      // 远程类默认走网关（凭证不落地），本地类保持直连
      gatewayMode: t.transport === 'http' ? true : f.gatewayMode,
    });
  };
  return (
    <div>
      {f.id === undefined && <TemplateGrid appliedKey={appliedTpl} onPick={applyTemplate} />}
      <div className="grid grid-cols-1 gap-x-6 md:grid-cols-2">
        <div>
          <Field label="名称" hint="保存时自动按名称生成 ID（即 agent 配置里的 server 键名）">
            <input data-testid="form-name" className={inputCls} value={f.name} onChange={(e) => set({ name: e.target.value })} placeholder="如：context7" />
          </Field>
          <Field label="传输类型">
            <select
              data-testid="form-transport"
              className={inputCls}
              value={f.transport}
              onChange={(e) => set({ transport: e.target.value as 'stdio' | 'http' | 'sse' })}
            >
              <option value="stdio">stdio（本机进程）</option>
              <option value="http">http（远程 streamable）</option>
              <option value="sse">sse（远程，旧协议）</option>
            </select>
          </Field>
          {f.transport === 'stdio' ? (
            <>
              <Field label="命令">
                <input data-testid="form-command" className={inputCls} value={f.command} onChange={(e) => set({ command: e.target.value })} placeholder="npx / uvx / node…" />
              </Field>
              <Field label="参数（空格分隔）" hint="含空格的路径用引号包裹，如 -y server-filesystem &quot;D:\my dir&quot;">
                <input data-testid="form-args" className={inputCls} value={f.args} onChange={(e) => set({ args: e.target.value })} placeholder="-y @upstash/context7-mcp" />
              </Field>
              <Field label="环境变量（每行 KEY=value）" hint="保存后静态加密（Windows DPAPI）；网关模式下不会写入 agent 配置。例：API_KEY=sk-xxx">
                <textarea data-testid="form-env" className={`${inputCls} h-24 font-mono`} value={f.env} onChange={(e) => set({ env: e.target.value })} placeholder={'API_KEY=sk-xxx\nBASE_URL=https://…'} />
              </Field>
            </>
          ) : (
            <Field label="URL" hint="streamable HTTP 端点一般以 /mcp 结尾；sse 仅在 server 只支持旧协议时使用。">
              <input data-testid="form-url" className={inputCls} value={f.url} onChange={(e) => set({ url: e.target.value })} placeholder="https://example.com/mcp" />
            </Field>
          )}
        </div>
        <div>
          <Field label="分发模式" hint={f.gatewayMode
            ? '当前：网关。Beaver 常驻代理此 server，凭证只留在本地。'
            : '当前：直连。配置原文写入各 agent，Beaver 不参与运行。'}>
            <div className="grid grid-cols-1 gap-2">
              <ModeCard
                selected={!f.gatewayMode}
                testId="form-direct"
                title="直连模式"
                desc="把完整配置写进各 agent 的配置文件，进程由各 agent 自己拉起。适合本地文件 / 终端类 server。"
                onClick={() => set({ gatewayMode: false })}
              />
              <ModeCard
                selected={f.gatewayMode}
                testId="form-gateway"
                title="网关模式"
                desc="agent 侧只写一条指向本地网关的 URL：凭证不落地，工具可逐个开关。适合远程 / 工具多的 server。"
                onClick={() => set({ gatewayMode: true })}
              />
            </div>
          </Field>
          <Field label="启用" hint="关闭 = 临时下线：不分发进 agent、不出现在网关端点；配置保留，随时可再打开。">
            <Toggle testId="form-enabled" checked={f.enabled} onChange={(v) => set({ enabled: v })} label="分发到各 agent，并在网关端点中提供" />
          </Field>
          {f.transport === 'stdio' && (
            <Field label="并发度（1-16）" hint="仅网关模式生效：此上游同时处理的工具调用数，1 最安全；直连模式下进程由 agent 自己管理，此项无效。">
              <input
                data-testid="form-concurrency"
                type="number"
                min={1}
                max={16}
                className={inputCls}
                value={f.concurrency}
                onChange={(e) => set({ concurrency: e.target.value })}
              />
            </Field>
          )}
        </div>
      </div>
    </div>
  );
}

function ToolsModal({ server, onClose }: { server: ServerDefDTO; onClose: () => void }) {
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const [debugTool, setDebugTool] = useState<string | null>(null);
  const [argsText, setArgsText] = useState('{}');
  const [result, setResult] = useState<string | null>(null);
  const [refresh, setRefresh] = useState(0);
  const tools = useQuery({
    queryKey: ['tools', server.id, refresh],
    queryFn: () => api.get<{ tools: ToolInfo[] }>(`/api/servers/${server.id}/tools${refresh > 0 ? '?refresh=1' : ''}`),
    retry: false,
    // 默认走服务端缓存，秒开；强制刷新时才需要等上游
    staleTime: refresh > 0 ? 0 : 60_000,
  });

  const toggle = useMutation({
    mutationFn: (toolOverrides: Record<string, { enabled: boolean }>) =>
      api.patch(`/api/servers/${server.id}`, { toolOverrides }),
    onSuccess: () => void qc.invalidateQueries(),
    onError: (e) => setError(String((e as Error).message)),
  });

  const invoke = useMutation({
    mutationFn: async ({ tool, args }: { tool: string; args: Record<string, unknown> }) =>
      api.post<{ result: unknown }>(`/api/servers/${server.id}/invoke`, { tool, arguments: args }),
    onSuccess: (res) => {
      setResult(JSON.stringify(res.result, null, 2));
      setError(null);
    },
    onError: (e) => {
      setResult(null);
      setError(`调用失败：${String((e as Error).message)}`);
    },
  });

  const runDebug = (): void => {
    if (debugTool === null) return;
    let args: Record<string, unknown> = {};
    try {
      args = argsText.trim() === '' ? {} : (JSON.parse(argsText) as Record<string, unknown>);
    } catch {
      setError('参数不是合法 JSON');
      return;
    }
    invoke.mutate({ tool: debugTool, args });
  };

  const overrides = server.toolOverrides ?? {};
  const setEnabled = (name: string, enabled: boolean): void => {
    toggle.mutate({ ...overrides, [name]: { enabled } });
  };

  return (
    <Modal title={`工具列表：${server.id}`} onClose={onClose} wide>
      <ErrorBanner message={error} />
      <div className="mb-3 flex items-center justify-between gap-2">
        <p className="text-xs text-slate-400">
          {tools.isPending
            ? '正在连接上游并获取工具列表；首次启动可能较慢（如 npx 需先下载包），已缓存时秒开。'
            : `共 ${tools.data?.tools.length ?? 0} 个工具；开关即时生效，勾选决定网关暴露哪些工具。`}
        </p>
        <Btn small disabled={tools.isPending} onClick={() => setRefresh((n) => n + 1)} title="忽略缓存，重新从上游抓取工具列表">
          刷新
        </Btn>
      </div>
      {tools.isError && (
        <p className="mb-3 text-sm text-amber-600">
          无法获取工具列表（{String((tools.error as Error).message)}）。网关需要能连接上游后才能列出工具。
        </p>
      )}
      {tools.isPending ? (
        <div className="flex items-center justify-center gap-2 py-6 text-sm text-slate-400">
          <Spinner className="h-4 w-4 text-brand-600" />
          正在连接上游并获取工具列表…
        </div>
      ) : (
        <table className="w-full text-sm">
          <tbody>
            {(tools.data?.tools ?? []).map((t) => (
              <tr key={t.name} className="border-b border-slate-100 transition-colors hover:bg-slate-50/60">
                <td className="py-2.5">
                  <div className="font-medium text-slate-800">{t.name}</div>
                  {t.description !== undefined && (
                    <div className="mt-0.5 text-xs leading-relaxed text-slate-400">{t.description}</div>
                  )}
                </td>
                <td className="w-16 py-2.5 text-right">
                  <Btn small onClick={() => { setDebugTool(t.name); setResult(null); setError(null); setArgsText('{}'); }}>
                    调试
                  </Btn>
                </td>
                <td className="w-20 py-2.5 text-right">
                  <label className="inline-flex cursor-pointer items-center gap-2">
                    <input
                      type="checkbox"
                      className="h-4 w-4"
                      checked={t.enabled}
                      onChange={(e) => setEnabled(t.name, e.target.checked)}
                    />
                    <span className="text-xs text-slate-500">{t.enabled ? '启用' : '停用'}</span>
                  </label>
                </td>
              </tr>
            ))}
            {(tools.data?.tools ?? []).length === 0 && (
              <tr>
                <td className="py-6 text-center text-slate-400">上游没有暴露任何工具。</td>
              </tr>
            )}
          </tbody>
        </table>
      )}

      {debugTool !== null && (
        <div data-testid="debug-panel" className="mt-4 rounded-xl border border-slate-200 bg-slate-50 p-4">
          <h4 className="mb-2 text-sm font-semibold text-slate-800">调试：{debugTool}</h4>
          <textarea
            data-testid="debug-args"
            className={`${inputCls} h-20 font-mono`}
            value={argsText}
            onChange={(e) => setArgsText(e.target.value)}
            placeholder='{"message": "hello"}'
          />
          <div className="mt-2 flex items-center gap-2">
            <Btn small kind="primary" disabled={invoke.isPending} onClick={runDebug}>
              {invoke.isPending ? (
                <>
                  <Spinner className="h-3.5 w-3.5" /> 调用中…
                </>
              ) : (
                '运行'
              )}
            </Btn>
          </div>
          {result !== null && (
            <pre
              data-testid="debug-result"
              className="mt-3 max-h-48 overflow-auto rounded-lg bg-slate-950 p-3 font-mono text-xs leading-relaxed text-slate-200 ring-1 ring-white/10"
            >
              {result}
            </pre>
          )}
        </div>
      )}
    </Modal>
  );
}
