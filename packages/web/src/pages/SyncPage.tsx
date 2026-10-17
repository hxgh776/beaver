import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import type { AgentInfo, GroupInfo, ServerDefDTO, SyncReport } from '../api';
import { Badge, Btn, Card, Chip, ConfirmDialog, Field, Modal, AddMenu, Pager, inputCls, useToast } from '../ui';

/** 明细分页：xl 4 列 × 1 行，配卡片头部的数字分页切换 */
const DETAIL_PAGE_SIZE = 4;
/** 分组卡片分页：3 列 × 2 行 */
const GROUP_PAGE_SIZE = 6;

export function SyncPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const [report, setReport] = useState<SyncReport | null>(null);
  const [dryRun, setDryRun] = useState(false);
  // 冲突处置：resolutionKey → override | skip
  const [resolutions, setResolutions] = useState<Record<string, 'override' | 'skip'>>({});
  const [editing, setEditing] = useState<GroupInfo | 'new' | null>(null);
  const [removing, setRemoving] = useState<GroupInfo | null>(null);
  const [detailPage, setDetailPage] = useState(0);
  const [groupPage, setGroupPage] = useState(0);

  const servers = useQuery({
    queryKey: ['servers'],
    queryFn: () => api.get<{ servers: ServerDefDTO[] }>('/api/servers'),
  });
  const agents = useQuery({
    queryKey: ['agents'],
    queryFn: () => api.get<{ agents: AgentInfo[] }>('/api/agents'),
  });
  const groups = useQuery({
    queryKey: ['groups'],
    queryFn: () => api.get<{ groups: GroupInfo[] }>('/api/groups'),
  });

  const invalidate = (): void => {
    void qc.invalidateQueries();
  };
  const wrap = (p: { onSuccess?: () => void }) => ({
    onSuccess: (): void => {
      p.onSuccess?.();
      invalidate();
    },
    onError: (e: Error): void => toast.err(String(e.message)),
  });

  const sync = useMutation({
    mutationFn: (isDryRun: boolean) => api.post<{ report: SyncReport }>('/api/sync', { dryRun: isDryRun, resolutions }),
    onSuccess: (res, isDryRun) => {
      setReport(res.report);
      setDryRun(isDryRun);
      toast.ok(isDryRun ? '预览完成（未落盘）' : '同步完成');
      if (!isDryRun) {
        setResolutions({});
        void qc.invalidateQueries();
      }
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const saveGroup = useMutation({
    mutationFn: (g: { id?: string; name: string; serverIds: string[]; agentTypes: string[] }) =>
      g.id !== undefined ? api.patch(`/api/groups/${g.id}`, g) : api.post('/api/groups', g),
    onSuccess: () => {
      setEditing(null);
      toast.ok('分组已保存（需要点「同步到所有 Agent」落盘）');
      invalidate();
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const deleteGroupMut = useMutation({
    mutationFn: (id: string) => api.del(`/api/groups/${id}`),
    onSuccess: () => {
      setRemoving(null);
      toast.ok('分组已删除（各 agent 在下次同步时收敛）');
      invalidate();
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const groupServer = useMutation({
    mutationFn: ({ groupId, serverId, on }: { groupId: string; serverId: string; on: boolean }) =>
      on ? api.put(`/api/groups/${groupId}/servers/${serverId}`) : api.del(`/api/groups/${groupId}/servers/${serverId}`),
    ...wrap({}),
  });
  const groupAgent = useMutation({
    mutationFn: ({ groupId, agentType, on }: { groupId: string; agentType: string; on: boolean }) =>
      on ? api.put(`/api/groups/${groupId}/agents/${agentType}`) : api.del(`/api/groups/${groupId}/agents/${agentType}`),
    ...wrap({}),
  });
  const bind = useMutation({
    mutationFn: ({ agentType, serverId, on }: { agentType: string; serverId: string; on: boolean }) =>
      on ? api.put(`/api/bindings/${agentType}/${serverId}`) : api.del(`/api/bindings/${agentType}/${serverId}`),
    ...wrap({}),
  });

  const serverList = servers.data?.servers ?? [];
  const agentList = agents.data?.agents ?? [];
  const groupList = groups.data?.groups ?? [];
  const serverName = (id: string): string => serverList.find((s) => s.id === id)?.name ?? id;
  const groupName = (id: string): string => groupList.find((g) => g.id === id)?.name ?? id;
  const agentName = (type: string): string => agentList.find((a) => a.agentType === type)?.displayName ?? type;
  // 明细分页：列表缩短时收敛到合法页码
  const detailTotalPages = Math.max(1, Math.ceil(agentList.length / DETAIL_PAGE_SIZE));
  const safeDetailPage = Math.min(detailPage, detailTotalPages - 1);
  const pageAgents = agentList.slice(safeDetailPage * DETAIL_PAGE_SIZE, (safeDetailPage + 1) * DETAIL_PAGE_SIZE);
  // 分组卡片分页
  const groupTotalPages = Math.max(1, Math.ceil(groupList.length / GROUP_PAGE_SIZE));
  const safeGroupPage = Math.min(groupPage, groupTotalPages - 1);
  const pageGroups = groupList.slice(safeGroupPage * GROUP_PAGE_SIZE, (safeGroupPage + 1) * GROUP_PAGE_SIZE);

  return (
    <div>
      <Card
        title="MCP 分组"
        subtitle={`共 ${serverList.length} 个 server · ${groupList.length} 个分组 —— agent 关联分组后即分发组内全部 server`}
        actions={
          <>
            <Btn testId="btn-dry-run" onClick={() => sync.mutate(true)}>
              预览变更
            </Btn>
            <Btn kind="primary" testId="btn-sync" disabled={sync.isPending} onClick={() => sync.mutate(false)}>
              同步到所有 Agent
            </Btn>
            <span className="mx-1 h-6 w-px bg-slate-200" aria-hidden />
            <Pager page={safeGroupPage} totalPages={groupTotalPages} onPage={setGroupPage} />
            <Btn small kind="primary" testId="btn-add-group" onClick={() => setEditing('new')}>
              新建分组
            </Btn>
          </>
        }
      >
        {groupList.length === 0 ? (
          <div className="flex flex-col items-center gap-3 py-6">
            <p className="text-sm text-slate-400">还没有分组——创建一个，例如「常用工具」「web 开发」。</p>
            <Btn small kind="primary" onClick={() => setEditing('new')}>
              新建第一个分组
            </Btn>
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-2.5 md:grid-cols-2 xl:grid-cols-3">
            {pageGroups.map((g) => (
              <div key={g.id} data-testid={`group-card-${g.id}`} className="flex flex-col rounded-xl border border-slate-200 px-3.5 py-3 transition-all duration-200 hover:-translate-y-0.5 hover:border-slate-300 hover:shadow-card-hover">
                <div className="mb-1 flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <span className="truncate text-sm font-semibold text-slate-800" title={g.name}>{g.name}</span>
                    <div className="mt-0.5 text-xs tabular-nums text-slate-400">
                      {g.serverIds.length} 个 MCP · {g.agentTypes.length} 个 agent
                    </div>
                  </div>
                  <div className="flex shrink-0 gap-1.5">
                    <Btn small onClick={() => setEditing(g)}>编辑</Btn>
                    <Btn small kind="danger" onClick={() => setRemoving(g)}>删除</Btn>
                  </div>
                </div>
                <div className="mt-1.5 flex flex-wrap items-center gap-1">
                  <span className="text-xs font-medium text-slate-400">包含</span>
                  {g.serverIds.map((sid) => (
                    <Chip key={sid} onRemove={() => groupServer.mutate({ groupId: g.id, serverId: sid, on: false })}>
                      {serverName(sid)}
                    </Chip>
                  ))}
                  {g.serverIds.length === 0 && <span className="text-xs text-slate-300">空分组</span>}
                </div>
                <div className="mt-1 mb-2 flex flex-wrap items-center gap-1">
                  <span className="text-xs font-medium text-slate-400">关联</span>
                  {g.agentTypes.map((t) => (
                    <Chip key={t} tone="blue" onRemove={() => groupAgent.mutate({ groupId: g.id, agentType: t, on: false })}>
                      {agentName(t)}
                    </Chip>
                  ))}
                  {g.agentTypes.length === 0 && <span className="text-xs text-slate-300">未关联</span>}
                </div>
                <div className="mt-auto flex flex-wrap gap-1.5 border-t border-slate-100 pt-2">
                  <AddMenu
                    label="添加 MCP"
                    testId={`group-server-add-${g.id}`}
                    options={serverList
                      .filter((s) => !g.serverIds.includes(s.id))
                      .map((s) => ({ value: s.id, label: s.name, hint: s.transport }))}
                    onPick={(sid) => groupServer.mutate({ groupId: g.id, serverId: sid, on: true })}
                  />
                  <AddMenu
                    label="关联 agent"
                    testId={`group-agent-add-${g.id}`}
                    optionTestId={(v) => `group-agent-option-${v}`}
                    options={agentList
                      .filter((a) => !g.agentTypes.includes(a.agentType))
                      .map((a) => ({ value: a.agentType, label: a.displayName, hint: a.detected ? undefined : '未发现' }))}
                    onPick={(t) => groupAgent.mutate({ groupId: g.id, agentType: t, on: true })}
                  />
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>

      <Card
        title="Agent 分发明细"
        subtitle="每个 agent 的有效分发 = 所属分组包含的 MCP + 直连添加的 MCP"
        actions={
          <Pager page={safeDetailPage} totalPages={detailTotalPages} onPage={setDetailPage} />
        }
      >
        <div className="grid grid-cols-1 gap-2.5 md:grid-cols-2 xl:grid-cols-4">
          {pageAgents.map((a) => (
            <div key={a.agentType} data-testid={`sync-agent-${a.agentType}`} className="flex flex-col rounded-xl border border-slate-200 px-3.5 py-3 transition-all duration-200 hover:-translate-y-0.5 hover:border-slate-300 hover:shadow-card-hover">
              <div className="flex items-center justify-between gap-2">
                <div className="flex min-w-0 items-center gap-1.5">
                  <span className="truncate text-sm font-semibold text-slate-800">{a.displayName}</span>
                  {a.detected ? (
                    <span title={`检测到配置文件：${a.configPaths[0]}`}>
                      <Badge tone="green">已安装</Badge>
                    </span>
                  ) : (
                    <span title="未发现配置文件">
                      <Badge tone="gray">未发现</Badge>
                    </span>
                  )}
                </div>
                <span className="shrink-0 text-xs tabular-nums text-slate-400">共 {a.boundServerIds.length} 个 MCP</span>
              </div>
              <div className="mt-1.5 flex flex-wrap items-center gap-1">
                <span className="text-xs font-medium text-slate-400">分组</span>
                {a.groupIds.map((gid) => (
                  <Chip key={gid} tone="blue" onRemove={() => groupAgent.mutate({ groupId: gid, agentType: a.agentType, on: false })}>
                    {groupName(gid)}
                  </Chip>
                ))}
                {a.groupIds.length === 0 && <span className="text-xs text-slate-300">无</span>}
                <span className="ml-1 text-xs font-medium text-slate-400">直连</span>
                {a.directServerIds.map((sid) => (
                  <Chip key={sid} onRemove={() => bind.mutate({ agentType: a.agentType, serverId: sid, on: false })}>
                    {serverName(sid)}
                  </Chip>
                ))}
                {a.directServerIds.length === 0 && <span className="text-xs text-slate-300">无</span>}
              </div>
              <div className="mt-auto flex flex-wrap gap-1.5 border-t border-slate-100 pt-2">
                <AddMenu
                  label="加入分组"
                  testId={`agent-group-add-${a.agentType}`}
                  options={groupList
                    .filter((g) => !g.agentTypes.includes(a.agentType))
                    .map((g) => ({ value: g.id, label: g.name, hint: `${g.serverIds.length} 个 MCP` }))}
                  onPick={(gid) => groupAgent.mutate({ groupId: gid, agentType: a.agentType, on: true })}
                />
                <AddMenu
                  label="直连添加 MCP"
                  testId={`agent-server-add-${a.agentType}`}
                  options={serverList
                    .filter((s) => !a.directServerIds.includes(s.id))
                    .map((s) => ({ value: s.id, label: s.name, hint: s.transport }))}
                  onPick={(sid) => bind.mutate({ agentType: a.agentType, serverId: sid, on: true })}
                />
              </div>
            </div>
          ))}
        </div>
      </Card>

      {editing !== null && (
        <GroupEditModal
          group={editing === 'new' ? null : editing}
          saving={saveGroup.isPending}
          servers={serverList}
          agents={agentList}
          onSave={(g) => saveGroup.mutate(g)}
          onClose={() => setEditing(null)}
        />
      )}
      {removing !== null && (
        <ConfirmDialog
          title="删除分组"
          message={`确定删除分组「${removing.name}」？组内 ${removing.serverIds.length} 个 MCP 不会被删除；已写入 agent 的配置会在下次同步时收敛清理。`}
          onConfirm={() => deleteGroupMut.mutate(removing.id)}
          onCancel={() => setRemoving(null)}
        />
      )}

      {report !== null && (
        <Card title={dryRun ? '变更预览（未落盘）' : '同步结果'}>
          {report.perAgent.map((a) => {
            const changes = a.changes.filter((c) => c.action !== 'none');
            if (changes.length === 0 && a.conflicts.length === 0 && a.unsupported.length === 0) return null;
            return (
              <div key={a.agentType} data-testid={`report-${a.agentType}`} className="mb-4">
                <h3 className="mb-1 font-medium">{a.agentType}</h3>
                <ul className="text-sm">
                  {changes.map((c) => (
                    <li key={c.key} className="text-slate-600">
                      <Badge tone={c.action === 'remove' ? 'red' : 'green'}>{c.action}</Badge> {c.key}
                    </li>
                  ))}
                  {a.unsupported.map((u) => (
                    <li key={u.serverId} className="text-amber-600">
                      ⚠ {u.serverId}: {u.reason}
                    </li>
                  ))}
                  {a.conflicts.map((c) => (
                    <li key={c.resolutionKey} data-testid={`conflict-${c.key}`} className="flex items-center gap-2 text-red-600">
                      ⚠ 冲突 {c.key}（检测到手工修改）
                      <select
                        className="rounded border border-slate-300 px-1 py-0.5 text-xs"
                        value={resolutions[c.resolutionKey] ?? 'skip'}
                        onChange={(e) =>
                          setResolutions({ ...resolutions, [c.resolutionKey]: e.target.value as 'override' | 'skip' })
                        }
                      >
                        <option value="skip">保留我的修改（跳过）</option>
                        <option value="override">用注册表覆盖</option>
                      </select>
                    </li>
                  ))}
                </ul>
              </div>
            );
          })}
          {dryRun && report.perAgent.some((a) => a.conflicts.length > 0) && (
            <p className="text-xs text-slate-400">选择冲突处置后，点「同步到所有 Agent」生效。</p>
          )}
        </Card>
      )}
    </div>
  );
}

function GroupEditModal(props: {
  group: GroupInfo | null;
  saving: boolean;
  servers: ServerDefDTO[];
  agents: AgentInfo[];
  onSave: (g: { id?: string; name: string; serverIds: string[]; agentTypes: string[] }) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState(props.group?.name ?? '');
  const [serverIds, setServerIds] = useState<string[]>(props.group?.serverIds ?? []);
  const [agentTypes, setAgentTypes] = useState<string[]>(props.group?.agentTypes ?? []);
  const toggle = (list: string[], v: string, set: (l: string[]) => void): void => {
    set(list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  };
  return (
    <Modal
      title={props.group !== null ? `编辑分组：${props.group.name}` : '新建分组'}
      onClose={props.onClose}
      footer={
        <>
          <Btn onClick={props.onClose}>取消</Btn>
          <Btn kind="primary" testId="group-save" disabled={props.saving || name.trim() === ''} onClick={() => props.onSave({ id: props.group?.id, name: name.trim(), serverIds, agentTypes })}>
            保存
          </Btn>
        </>
      }
    >
      <Field label="分组名称">
        <input data-testid="group-name" className={inputCls} value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：常用工具" />
      </Field>
      <Field label={`包含的 MCP（${serverIds.length} 个）`}>
        <div className="max-h-48 space-y-0.5 overflow-y-auto rounded-lg border border-slate-200 p-2">
          {props.servers.length === 0 && <p className="px-1 py-0.5 text-xs text-slate-400">注册表为空，先去「MCP 发现」或「MCP 管理」添加。</p>}
          {props.servers.map((s) => (
            <label key={s.id} className="flex cursor-pointer items-center gap-2 rounded-md px-1.5 py-1 text-sm transition-colors hover:bg-slate-50">
              <input
                type="checkbox"
                className="h-4 w-4"
                data-testid={`group-server-${s.id}`}
                checked={serverIds.includes(s.id)}
                onChange={() => toggle(serverIds, s.id, setServerIds)}
              />
              <span className="font-medium text-slate-700">{s.name}</span>
              <span className="font-mono text-xs text-slate-400">{s.id}</span>
            </label>
          ))}
        </div>
      </Field>
      <Field label={`关联的 agent（${agentTypes.length} 个）`} hint="关联后该 agent 即分发组内全部 MCP">
        <div className="max-h-48 space-y-0.5 overflow-y-auto rounded-lg border border-slate-200 p-2">
          {props.agents.map((a) => (
            <label key={a.agentType} className="flex cursor-pointer items-center gap-2 rounded-md px-1.5 py-1 text-sm transition-colors hover:bg-slate-50">
              <input
                type="checkbox"
                className="h-4 w-4"
                data-testid={`group-agent-${a.agentType}`}
                checked={agentTypes.includes(a.agentType)}
                onChange={() => toggle(agentTypes, a.agentType, setAgentTypes)}
              />
              <span className="font-medium text-slate-700">{a.displayName}</span>
              {!a.detected && <span className="text-xs text-slate-300">未发现</span>}
            </label>
          ))}
        </div>
      </Field>
    </Modal>
  );
}
