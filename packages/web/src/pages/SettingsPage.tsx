import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import type { AutostartInfo, BackupGroups, SettingsInfo, StatusInfo } from '../api';
import { Badge, Btn, Card, ErrorBanner, Field, inputCls, OkBanner, selectCls, Toggle, useToast } from '../ui';

/** 设置 tab（docs/desktop-plan.md §5）：零 JSON 的最后一块。
 * 端口（改后提示重启）· 日志级别 · 开机自启开关 · 恢复备份（agent 配置 + 管理配置自身）· 打开数据目录 · 关于。 */
export function SettingsPage() {
  const toast = useToast();
  const queryClient = useQueryClient();
  const status = useQuery({
    queryKey: ['status'],
    queryFn: () => api.get<StatusInfo>('/api/status'),
    refetchInterval: 10_000,
  });
  const settings = useQuery({
    queryKey: ['settings'],
    queryFn: () => api.get<SettingsInfo>('/api/settings'),
  });
  const autostart = useQuery({
    queryKey: ['autostart'],
    queryFn: () => api.get<AutostartInfo>('/api/autostart'),
  });
  const backups = useQuery({
    queryKey: ['backups'],
    queryFn: () => api.get<BackupGroups>('/api/backups'),
  });

  // —— 端口 / 日志级别 ——
  const [portDraft, setPortDraft] = useState<string | null>(null);
  const savePort = useMutation({
    mutationFn: (port: number) => api.patch('/api/settings', { port }),
    onSuccess: () => {
      toast.ok('端口已保存——重启 daemon 后生效：beaver stop && beaver start');
      void queryClient.invalidateQueries({ queryKey: ['settings'] });
    },
    onError: (e) => toast.err(`保存失败：${String(e)}`),
  });
  const saveLogLevel = useMutation({
    mutationFn: (logLevel: string) => api.patch('/api/settings', { logLevel }),
    onSuccess: () => {
      toast.ok('日志级别已生效');
      void queryClient.invalidateQueries({ queryKey: ['settings'] });
    },
    onError: (e) => toast.err(`保存失败：${String(e)}`),
  });

  // —— 开机自启 ——
  const setAutostart = useMutation({
    mutationFn: (installed: boolean) => api.put<AutostartInfo>('/api/autostart', { installed }),
    onSuccess: (data) => {
      toast.ok(data.installed ? '已安装开机自启，下次登录自动拉起' : '已移除开机自启');
      void queryClient.invalidateQueries({ queryKey: ['autostart'] });
    },
    onError: (e) => toast.err(`操作失败：${String(e)}`),
  });

  // —— 恢复备份 ——
  const [agentSel, setAgentSel] = useState('');
  const [fileSel, setFileSel] = useState('');
  const groups = backups.data?.groups ?? [];
  const currentGroup = groups.find((g) => g.agentType === agentSel) ?? groups[0];
  const files = currentGroup?.files ?? [];
  const activeFile = fileSel !== '' && files.some((f) => f.name === fileSel) ? fileSel : (files[0]?.name ?? '');
  const restore = useMutation({
    mutationFn: (p: { agentType: string; name: string }) => api.post('/api/backups/restore', p),
    onSuccess: () => {
      toast.ok(`已恢复 ${activeFile}（该 agent 的同步账本已清，下次同步不再弹冲突）`);
      void queryClient.invalidateQueries({ queryKey: ['backups'] });
    },
    onError: (e) => toast.err(`恢复失败：${String(e)}`),
  });
  const [configFileSel, setConfigFileSel] = useState('');
  const configBackups = backups.data?.configBackups ?? [];
  const activeConfigFile =
    configFileSel !== '' && configBackups.some((f) => f.name === configFileSel)
      ? configFileSel
      : (configBackups[0]?.name ?? '');
  const restoreConfig = useMutation({
    mutationFn: (name: string) => api.post('/api/backups/restore', { name, config: true }),
    onSuccess: () => {
      toast.ok('管理配置已回滚并重载');
      void queryClient.invalidateQueries();
    },
    onError: (e) => toast.err(`恢复失败：${String(e)}`),
  });

  // —— 打开数据目录 ——
  const openDir = useMutation({
    mutationFn: () => api.post('/api/settings/open-dir'),
    onError: (e) => toast.err(`打开失败：${String(e)}`),
  });

  const s = settings.data;
  return (
    <div className="space-y-4">
      <Card
        title="常规"
        subtitle="端口改后需重启 daemon；令牌与网关开关在「聚合网关」页"
      >
        <div className="grid gap-4 md:grid-cols-2">
          <Field label="监听端口" hint="改后提示重启；端口被占用时会自动漂移并持久化">
            <div className="flex items-center gap-2">
              <input
                data-testid="settings-port"
                className={`${inputCls} w-28 font-mono`}
                type="number"
                min={1}
                max={65535}
                value={portDraft ?? String(s?.port ?? '')}
                onChange={(e) => setPortDraft(e.target.value)}
              />
              <Btn
                kind="primary"
                disabled={portDraft === null || Number(portDraft) === s?.port || !/^\d+$/.test(portDraft)}
                onClick={() => savePort.mutate(Number(portDraft))}
              >
                保存
              </Btn>
              {(status.data?.port ?? 0) !== (status.data?.configuredPort ?? 0) && (
                <Badge tone="amber">实际端口已漂移到 {status.data?.port}</Badge>
              )}
            </div>
          </Field>
          <Field label="日志级别">
            <select
              data-testid="settings-loglevel"
              className={selectCls}
              value={s?.logLevel ?? 'info'}
              onChange={(e) => saveLogLevel.mutate(e.target.value)}
            >
              <option value="debug">debug</option>
              <option value="info">info</option>
              <option value="warn">warn</option>
              <option value="error">error</option>
            </select>
          </Field>
          <Field label="开机自启" hint="登录时静默拉起 daemon（start 幂等，已在运行则直接返回）">
            <Toggle
              testId="settings-autostart"
              checked={autostart.data?.installed === true}
              disabled={setAutostart.isPending}
              onChange={(v) => setAutostart.mutate(v)}
            />
          </Field>
          <Field label="数据目录" hint="config.json、skills、备份都在这里">
            <div className="flex items-center gap-2">
              <code className="max-w-72 truncate rounded bg-slate-100 px-2 py-1 font-mono text-xs text-slate-600">
                {status.data?.homeDir}
              </code>
              <Btn onClick={() => openDir.mutate()}>打开目录</Btn>
            </div>
          </Field>
        </div>
      </Card>

      <Card
        title="恢复备份"
        subtitle="同步引擎每次落盘前的滚动备份都在这里；恢复后清同步账本，不会误弹冲突。改坏配置时的后悔药。"
      >
        <ErrorBanner message={restore.error !== null ? String(restore.error) : null} />
        <div className="grid gap-4 md:grid-cols-2">
          <div>
            <div className="mb-2 text-xs font-medium text-slate-500">Agent 配置备份</div>
            {groups.length === 0 ? (
              <p className="text-sm text-slate-400">还没有任何 agent 配置备份（首次同步后生成）。</p>
            ) : (
              <div className="flex items-center gap-2">
                <select
                  data-testid="backup-agent"
                  className={selectCls}
                  value={currentGroup?.agentType ?? ''}
                  onChange={(e) => {
                    setAgentSel(e.target.value);
                    setFileSel('');
                  }}
                >
                  {groups.map((g) => (
                    <option key={g.agentType} value={g.agentType}>
                      {g.agentType}（{g.files.length} 份）
                    </option>
                  ))}
                </select>
                <select
                  data-testid="backup-file"
                  className={selectCls}
                  value={activeFile}
                  onChange={(e) => setFileSel(e.target.value)}
                >
                  {files.map((f) => (
                    <option key={f.name} value={f.name}>
                      {f.name}
                    </option>
                  ))}
                </select>
                <Btn
                  kind="primary"
                  testId="btn-restore"
                  disabled={restore.isPending || activeFile === ''}
                  onClick={() =>
                    restore.mutate({ agentType: currentGroup?.agentType ?? '', name: activeFile })
                  }
                >
                  恢复
                </Btn>
              </div>
            )}
          </div>
          <div>
            <div className="mb-2 text-xs font-medium text-slate-500">管理配置自身（config.json，写前滚动 keep=10）</div>
            {configBackups.length === 0 ? (
              <p className="text-sm text-slate-400">还没有配置备份（第二次保存后生成）。</p>
            ) : (
              <div className="flex items-center gap-2">
                <select
                  data-testid="backup-config-file"
                  className={selectCls}
                  value={activeConfigFile}
                  onChange={(e) => setConfigFileSel(e.target.value)}
                >
                  {configBackups.map((f) => (
                    <option key={f.name} value={f.name}>
                      {f.name}
                    </option>
                  ))}
                </select>
                <Btn
                  kind="primary"
                  disabled={restoreConfig.isPending || activeConfigFile === ''}
                  onClick={() => restoreConfig.mutate(activeConfigFile)}
                >
                  回滚
                </Btn>
              </div>
            )}
          </div>
        </div>
        {restore.isSuccess && <OkBanner message="恢复完成。" />}
        {restoreConfig.isSuccess && <OkBanner message="配置已回滚。" />}
      </Card>

      <Card title="关于" subtitle="Beaver —— Every agent's gear. One dam.">
        <div className="space-y-2 text-sm text-slate-600">
          <p>
            版本 v{status.data?.version} · 运行 {Math.floor((status.data?.uptimeSec ?? 0) / 60)} 分钟 · 数据目录{' '}
            <code className="rounded bg-slate-100 px-1 py-0.5 font-mono text-xs">{status.data?.homeDir}</code>
          </p>
          <p className="text-xs leading-relaxed text-slate-400">
            卸载说明：先在上方关闭开机自启并停止 daemon（<code className="font-mono">beaver stop</code>）→ 删除可执行文件
            → 删除数据目录（上面路径）。exe 版无卸载器，路径即全部。
          </p>
        </div>
      </Card>
    </div>
  );
}
