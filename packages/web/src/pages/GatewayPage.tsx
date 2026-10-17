import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, setToken } from '../api';
import type { AgentInfo, GatewayInfo, StatusInfo } from '../api';
import { Badge, Btn, Card, CopyBtn, Toggle, codeChipCls, rowCls, theadCls, useToast } from '../ui';
import type { TabKey } from '../App';

export function GatewayPage({ onNavigate }: { onNavigate?: (tab: TabKey) => void }) {
  const qc = useQueryClient();
  const toast = useToast();

  const status = useQuery({
    queryKey: ['status'],
    queryFn: () => api.get<StatusInfo>('/api/status'),
  });
  const gateway = useQuery({
    queryKey: ['gateway'],
    queryFn: () => api.get<GatewayInfo>('/api/gateway'),
    refetchInterval: 5000,
  });
  const token = useQuery({
    queryKey: ['token'],
    queryFn: () => api.get<{ token: string }>('/api/settings/token'),
    enabled: status.data?.authRequired === true,
  });
  const agents = useQuery({
    queryKey: ['agents'],
    queryFn: () => api.get<{ agents: AgentInfo[] }>('/api/agents'),
  });

  const control = useMutation({
    mutationFn: (action: 'start' | 'stop') => api.post(`/api/gateway/${action}`),
    onSuccess: () => {
      void qc.invalidateQueries();
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const rotate = useMutation({
    mutationFn: () => api.post<{ token: string; resyncRequired: boolean }>('/api/settings/token/rotate'),
    onSuccess: (res) => {
      void qc.invalidateQueries();
      toast.ok(res.resyncRequired ? 'token 已轮换——已分发的网关配置需要重新同步！' : 'token 已轮换');
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const authRequired = status.data?.authRequired === true;
  const toggleAuth = useMutation({
    mutationFn: (on: boolean) => api.patch('/api/settings', { authRequired: on }),
    onSuccess: async (_res, on) => {
      if (on) {
        // 把当前 token 写入本浏览器，避免开启后自锁在令牌门外
        try {
          const t = await api.get<{ token: string }>('/api/settings/token');
          setToken(t.token);
        } catch { /* 忽略 */ }
      }
      void qc.invalidateQueries();
      toast.ok(on ? '访问令牌已启用——请到「Agent 分发」页重新同步（分发内容将携带凭证）' : '访问令牌已关闭——请到「Agent 分发」页重新同步（去除凭证）');
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const restart = useMutation({
    mutationFn: ({ id, action }: { id: string; action: 'start' | 'stop' | 'restart' }) =>
      api.post(`/api/servers/${id}/${action}`),
    onSuccess: () => void qc.invalidateQueries(),
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const gw = gateway.data;
  const endpoint = gw?.running === true && gw.port !== undefined ? `http://127.0.0.1:${gw.port}/mcp` : null;
  const boundAgents = (agents.data?.agents ?? []).filter((a) => a.boundServerIds.length > 0);

  return (
    <div>
      {status.data?.resyncPending === true && (
        <div data-testid="resync-banner" className="mb-4 rounded border border-amber-300 bg-amber-50 p-3 text-sm text-amber-800">
          ⚠ token 已轮换或访问令牌开关已变更，已分发的网关配置需要重新同步——请到
          <button className="mx-1 underline" onClick={() => onNavigate?.('sync')}>
            「Agent 分发」页
          </button>
          操作，此提醒才会消除。
        </div>
      )}

      <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-2">
        <Card
          title="聚合端点"
          actions={
            gw?.running === true ? (
              <Btn testId="btn-gateway-stop" onClick={() => control.mutate('stop')}>
                停止网关
              </Btn>
            ) : (
              <Btn kind="primary" testId="btn-gateway-start" onClick={() => control.mutate('start')}>
                启动网关
              </Btn>
            )
          }
        >
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            {gw?.running === true ? <Badge tone="green">运行中</Badge> : <Badge tone="gray">已停止</Badge>}
            {endpoint !== null && (
              <>
                <code data-testid="endpoint-url" className={`min-w-0 flex-1 ${codeChipCls}`}>
                  {endpoint}
                </code>
                <CopyBtn text={endpoint} />
              </>
            )}
          </div>
          <p className="text-xs text-slate-400">
            把这个端点配到任意 agent（Authorization: Bearer &lt;token&gt;），即可使用所有「网关模式」server 的工具。
          </p>
        </Card>

        <Card
          title="访问令牌"
          actions={
            <Toggle
              testId="auth-toggle"
              checked={authRequired}
              onChange={(v) => toggleAuth.mutate(v)}
              label={authRequired ? '已启用' : '未启用'}
            />
          }
        >
          {authRequired ? (
            <>
              <div className="flex items-center gap-2">
                <code data-testid="token-value" className={`min-w-0 flex-1 ${codeChipCls}`}>
                  {token.data?.token}
                </code>
                <CopyBtn text={token.data?.token ?? ''} />
              </div>
              <div className="mt-2 flex items-center gap-2">
                <Btn small kind="danger" onClick={() => rotate.mutate()}>
                  轮换 token
                </Btn>
                <span className="text-xs text-slate-400">
                  轮换后所有已分发的网关配置都会失效，需要到「Agent 分发」页重新同步。
                </span>
              </div>
            </>
          ) : (
            <p className="text-xs text-slate-500">
              访问令牌未启用（默认）：网关与 API 仅监听 127.0.0.1，本机进程即可访问。
              若本机环境不受信任，建议开启；开启后到「Agent 分发」页重新同步，分发内容会携带凭证。
            </p>
          )}
        </Card>
      </div>

      <Card title="Agent 分组端点" subtitle="每个 agent 只看到自己有效绑定的网关 server——同步时写入的就是该 agent 的专属 URL">
        <table className="w-full text-sm">
          <tbody>
            {boundAgents.map((a) => {
              const agentEndpoint = `http://127.0.0.1:${status.data?.port}/agents/${a.agentType}/mcp`;
              return (
                <tr key={a.agentType} className={rowCls}>
                  <td className="w-40 py-2 font-medium text-slate-800">{a.displayName}</td>
                  <td className="py-2">
                    <div className="flex items-center gap-2">
                      <code className={`block min-w-0 flex-1 break-all ${codeChipCls}`}>{agentEndpoint}</code>
                      <CopyBtn text={agentEndpoint} />
                    </div>
                  </td>
                  <td className="w-28 py-2 text-right text-xs tabular-nums text-slate-500">
                    绑定 {a.boundServerIds.length} 个
                  </td>
                </tr>
              );
            })}
            {boundAgents.length === 0 && (
              <tr>
                <td className="py-4 text-center text-slate-400" colSpan={3}>
                  暂无绑定——到「Agent 分发」页勾选或关联分组后，这里会出现各 agent 的分组端点。
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Card>

      <Card title="上游进程" subtitle="懒启动：首次调用时自动拉起">
        <table className="w-full text-sm">
          <thead>
            <tr className={theadCls}>
              <th className="py-2">Server</th>
              <th className="py-2">状态</th>
              <th className="py-2">PID</th>
              <th className="py-2">工具数</th>
              <th className="py-2">错误</th>
              <th className="py-2 text-right">操作</th>
            </tr>
          </thead>
          <tbody>
            {(gw?.upstreams ?? []).map((u) => {
              // 按进程状态禁用无意义操作：ready 时无须再启动，已停止/出错时无从停止
              const alive = u.status === 'ready' || u.status === 'starting' || u.status === 'backoff';
              return (
                <tr key={u.id} data-testid={`upstream-${u.id}`} className={rowCls}>
                  <td className="py-2 font-medium text-slate-800">{u.id}</td>
                  <td className="py-2">
                    <UpstreamBadge status={u.status} />
                  </td>
                  <td className="py-2 tabular-nums text-slate-600">{u.pid ?? '-'}</td>
                  <td className="py-2 tabular-nums text-slate-600">{u.toolCount}</td>
                  <td className="py-2 text-xs text-rose-600">{u.lastError ?? ''}</td>
                  <td className="py-2 text-right">
                    <div className="flex justify-end gap-2">
                      <Btn small disabled={alive} onClick={() => restart.mutate({ id: u.id, action: 'start' })}>
                        启动
                      </Btn>
                      <Btn small disabled={u.status !== 'ready'} onClick={() => restart.mutate({ id: u.id, action: 'restart' })}>
                        重启
                      </Btn>
                      <Btn small disabled={!alive} onClick={() => restart.mutate({ id: u.id, action: 'stop' })}>
                        停止
                      </Btn>
                    </div>
                  </td>
                </tr>
              );
            })}
            {(gw?.upstreams ?? []).length === 0 && (
              <tr>
                <td colSpan={6} className="py-6 text-center text-slate-400">
                  暂无已连接的上游（懒启动：首次调用时自动拉起）。
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Card>
    </div>
  );
}

function UpstreamBadge({ status }: { status: string }) {
  if (status === 'ready') return <Badge tone="green">ready</Badge>;
  if (status === 'starting') return <Badge tone="blue">starting</Badge>;
  if (status === 'backoff') return <Badge tone="amber">backoff</Badge>;
  if (status === 'error') return <Badge tone="red">error</Badge>;
  return <Badge tone="gray">{status}</Badge>;
}
