import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import type { ProviderApp, ProviderProfileDTO, ProviderPresetDTO } from '../api';
import { Badge, Btn, Card, ConfirmDialog, ErrorBanner, Modal, Segmented, Spinner, AddMenu, inputCls, useToast } from '../ui';

const APPS: Array<{ app: ProviderApp; label: string; hint: string }> = [
  { app: 'claude-code', label: 'Claude Code', hint: '写入 ~/.claude/settings.json（只动供应商键，hooks/permissions 不碰）' },
  { app: 'codex', label: 'Codex CLI', hint: '写入 ~/.codex/config.toml + auth.json（原生登录自动暂存/恢复）' },
  { app: 'gemini-cli', label: 'Gemini CLI', hint: '写入 ~/.gemini/.env 与 settings.json' },
];

export function ProvidersPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const [app, setApp] = useState<ProviderApp>('claude-code');
  const [creating, setCreating] = useState(false);
  const [removing, setRemoving] = useState<ProviderProfileDTO | null>(null);
  const [switchingNote, setSwitchingNote] = useState<ProviderProfileDTO | null>(null);

  const providers = useQuery({
    queryKey: ['providers'],
    queryFn: () => api.get<{ apps: Array<{ app: ProviderApp; current: string | null; profiles: ProviderProfileDTO[] }> }>('/api/providers'),
  });

  const invalidate = (): void => {
    void qc.invalidateQueries({ queryKey: ['providers'] });
  };

  const currentApp = providers.data?.apps.find((a) => a.app === app);

  const doSwitch = useMutation({
    mutationFn: (p: { id: string; preserveCodexLogin?: boolean }) =>
      api.post<{ app: ProviderApp; changed: string[] }>(`/api/providers/${p.id}/switch`, {
        preserveCodexLogin: p.preserveCodexLogin,
      }),
    onSuccess: (res) => {
      toast.ok(`已切换（写入 ${res.changed.length} 个文件）。Claude Code / Codex 需重启客户端生效。`);
      setSwitchingNote(null);
      invalidate();
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  const doDelete = useMutation({
    mutationFn: (id: string) => api.del(`/api/providers/${id}`),
    onSuccess: () => {
      toast.ok('已删除供应商档案');
      setRemoving(null);
      invalidate();
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2" data-testid="provider-app-tabs">
        <Segmented
          value={app}
          onChange={setApp}
          options={APPS.map((a) => ({
            value: a.app,
            testId: `provider-app-${a.app}`,
            title: a.hint,
            label: a.label,
          }))}
        />
        <Btn kind="primary" testId="btn-provider-new" className="ml-auto" onClick={() => setCreating(true)}>
          新建供应商
        </Btn>
      </div>

      <p className="text-xs text-slate-500">{APPS.find((a) => a.app === app)!.hint}</p>

      {(currentApp?.profiles.length ?? 0) === 0 ? (
        <Card className="p-8 text-center text-sm text-slate-400">
          还没有{APPS.find((a) => a.app === app)!.label}的供应商档案——从 90+ 预设一键创建，填入 API Key 即可切换。
        </Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {currentApp!.profiles.map((p) => {
            const isCurrent = currentApp!.current === p.id;
            return (
              <div
                key={p.id}
                data-testid={`provider-card-${p.id}`}
                className={`flex flex-col rounded-xl border p-4 transition-all duration-200 hover:-translate-y-0.5 hover:shadow-card-hover ${
                  isCurrent ? 'border-brand-400 bg-brand-50/40 shadow-card' : 'border-slate-200 hover:border-slate-300'
                }`}
              >
                <div className="flex items-start gap-2">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="truncate text-sm font-semibold text-slate-800">{p.name}</span>
                      {isCurrent && <Badge tone="green">当前</Badge>}
                      {p.category && <Badge tone="gray">{p.category}</Badge>}
                      {p.fromPreset && <Badge tone="blue">预设</Badge>}
                    </div>
                    {p.websiteUrl && (
                      <div className="mt-1 truncate text-xs text-slate-400">{p.websiteUrl}</div>
                    )}
                  </div>
                </div>
                <div className="mt-3 flex items-center gap-2 border-t border-slate-100 pt-3">
                  {isCurrent ? (
                    <span className="text-xs text-slate-400">正在使用</span>
                  ) : (
                    <Btn kind="primary" testId={`provider-switch-${p.id}`} disabled={doSwitch.isPending} onClick={() => setSwitchingNote(p)}>
                      一键切换
                    </Btn>
                  )}
                  <Btn kind="danger" testId={`provider-delete-${p.id}`} onClick={() => setRemoving(p)}>
                    删除
                  </Btn>
                </div>
              </div>
            );
          })}
        </div>
      )}

      {creating && (
        <CreateProviderModal
          app={app}
          onClose={() => setCreating(false)}
          onSaved={(name) => {
            setCreating(false);
            toast.ok(`已创建 ${name}`);
            invalidate();
          }}
        />
      )}

      {switchingNote && (
        <ConfirmDialog
          title="切换供应商"
          message={
            switchingNote.app === 'codex'
              ? `将写入 ~/.codex/config.toml 与 auth.json（原生登录会自动暂存，切回时恢复）。切换后请重启 Codex CLI。`
              : `将投影写入对应配置文件（只动供应商拥有的键）。切换后请重启客户端生效。`
          }
          confirmText="确认切换"
          onCancel={() => setSwitchingNote(null)}
          onConfirm={() => doSwitch.mutate({ id: switchingNote.id })}
        />
      )}

      {removing && (
        <ConfirmDialog
          title="删除供应商"
          message={`删除档案「${removing.name}」？已写入客户端的配置不受影响，仅删除本工具内的档案。`}
          confirmText="删除"
          onCancel={() => setRemoving(null)}
          onConfirm={() => doDelete.mutate(removing.id)}
        />
      )}
    </div>
  );
}

function CreateProviderModal({
  app,
  onClose,
  onSaved,
}: {
  app: ProviderApp;
  onClose: () => void;
  onSaved: (name: string) => void;
}) {
  const [mode, setMode] = useState<'preset' | 'custom'>('preset');
  const [presetName, setPresetName] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [customName, setCustomName] = useState('');
  const [customBaseUrl, setCustomBaseUrl] = useState('');
  const [customModel, setCustomModel] = useState('');
  const [error, setError] = useState<string | null>(null);

  const presets = useQuery({
    queryKey: ['provider-presets', app],
    queryFn: () => api.get<{ presets: ProviderPresetDTO[] }>(`/api/providers/presets?app=${app}`),
  });

  const fromPreset = useMutation({
    mutationFn: () =>
      api.post<{ profile: ProviderProfileDTO }>('/api/providers/from-preset', {
        app,
        presetName,
        apiKey: apiKey.trim() || undefined,
      }),
    onSuccess: (res) => onSaved(res.profile.name),
    onError: (e) => setError(String((e as Error).message)),
  });

  const custom = useMutation({
    mutationFn: () => {
      const name = customName.trim();
      const base = customBaseUrl.trim().replace(/\/+$/, '');
      const key = apiKey.trim();
      const model = customModel.trim();
      const config =
        app === 'codex'
          ? {
              auth: { OPENAI_API_KEY: key },
              configToml: `model_provider = "custom"\n${model ? `model = "${model}"\n` : ''}model_reasoning_effort = "high"\ndisable_response_storage = true\n\n[model_providers.custom]\nname = "custom"\nbase_url = "${base}"\nwire_api = "responses"\n`,
            }
          : app === 'claude-code'
            ? { env: { ANTHROPIC_BASE_URL: base, ...(key ? { ANTHROPIC_AUTH_TOKEN: key } : {}), ...(model ? { ANTHROPIC_MODEL: model } : {}) } }
            : { env: { GOOGLE_GEMINI_BASE_URL: base, ...(key ? { GEMINI_API_KEY: key } : {}), ...(model ? { GEMINI_MODEL: model } : {}) } };
      return api.post<{ profile: ProviderProfileDTO }>('/api/providers', {
        app,
        name,
        category: 'custom',
        config,
      });
    },
    onSuccess: (res) => onSaved(res.profile.name),
    onError: (e) => setError(String((e as Error).message)),
  });

  const preset = presets.data?.presets.find((p) => p.name === presetName);

  return (
    <Modal title="新建供应商" onClose={onClose} footer={null}>
      <div className="mb-4 flex gap-2">
        <Btn small testId="provider-mode-preset" onClick={() => setMode('preset')} kind={mode === 'preset' ? 'primary' : 'default'}>
          从预设
        </Btn>
        <Btn small testId="provider-mode-custom" onClick={() => setMode('custom')} kind={mode === 'custom' ? 'primary' : 'default'}>
          自定义
        </Btn>
      </div>
      {error && <ErrorBanner message={error} />}
      {mode === 'preset' ? (
        <div className="space-y-3">
          <div>
            <label className="mb-1 block text-xs font-medium text-slate-600">
              预设（{presets.data?.presets.length ?? 0} 个，输入即可筛选）
            </label>
            {preset === undefined ? (
              <AddMenu
                label="选择预设…"
                testId="provider-preset-select"
                optionTestId={(v) => `provider-preset-option-${v}`}
                direction="down"
                options={(presets.data?.presets ?? []).map((p) => ({
                  value: p.name,
                  label: p.name,
                  hint: p.isOfficial ? '官方' : p.category,
                }))}
                onPick={(v) => setPresetName(v)}
              />
            ) : (
              <div className="flex items-center gap-2 rounded-lg border border-brand-200 bg-brand-50 px-3 py-2 text-sm">
                <span className="font-medium text-brand-800">{preset.name}</span>
                {preset.isOfficial && <Badge tone="blue">官方</Badge>}
                {preset.category && <Badge tone="gray">{preset.category}</Badge>}
                <button
                  className="ml-auto text-xs text-slate-400 transition-colors hover:text-slate-600"
                  onClick={() => setPresetName('')}
                >
                  重选
                </button>
              </div>
            )}
          </div>
          <div>
            <label className="mb-1 block text-xs font-medium text-slate-600">API Key（占位键会被替换；留空则切换前需再编辑）</label>
            <input
              data-testid="provider-key-input"
              type="password"
              className={inputCls}
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && presetName !== '' && !fromPreset.isPending) fromPreset.mutate();
              }}
              placeholder="sk-…"
            />
          </div>
          <Btn
            kind="primary"
            testId="provider-save"
            disabled={fromPreset.isPending || presetName === ''}
            onClick={() => fromPreset.mutate()}
          >
            {fromPreset.isPending ? <Spinner className="h-4 w-4" /> : '创建'}
          </Btn>
        </div>
      ) : (
        <div className="space-y-3">
          <div>
            <label className="mb-1 block text-xs font-medium text-slate-600">名称</label>
            <input data-testid="provider-custom-name" className={inputCls} value={customName} onChange={(e) => setCustomName(e.target.value)} placeholder="例如：My Relay" />
          </div>
          <div>
            <label className="mb-1 block text-xs font-medium text-slate-600">Base URL</label>
            <input data-testid="provider-custom-baseurl" className={inputCls} value={customBaseUrl} onChange={(e) => setCustomBaseUrl(e.target.value)} placeholder="https://…" />
          </div>
          <div>
            <label className="mb-1 block text-xs font-medium text-slate-600">API Key</label>
            <input type="password" className={inputCls} value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder="sk-…" />
          </div>
          <div>
            <label className="mb-1 block text-xs font-medium text-slate-600">模型（可选）</label>
            <input
              className={inputCls}
              value={customModel}
              onChange={(e) => setCustomModel(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && customName.trim() !== '' && customBaseUrl.trim() !== '' && !custom.isPending) custom.mutate();
              }}
            />
          </div>
          <Btn kind="primary" disabled={custom.isPending || customName.trim() === '' || customBaseUrl.trim() === ''} onClick={() => custom.mutate()}>
            {custom.isPending ? <Spinner className="h-4 w-4" /> : '创建'}
          </Btn>
        </div>
      )}
    </Modal>
  );
}
