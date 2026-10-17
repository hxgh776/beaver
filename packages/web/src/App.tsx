import { useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError, api, getToken, setToken } from './api';
import type { StatusInfo } from './api';
import { BrandMark, Icon, Spinner, ToastProvider } from './ui';
import { StatusPage } from './pages/StatusPage';
import { ServersPage } from './pages/ServersPage';
import { SyncPage } from './pages/SyncPage';
import { GatewayPage } from './pages/GatewayPage';
import { LogsPage } from './pages/LogsPage';
import { DiscoveryPage } from './pages/DiscoveryPage';
import { SkillsPage } from './pages/SkillsPage';
import { ProvidersPage } from './pages/ProvidersPage';

const TABS = [
  { key: 'status', label: '概览', icon: 'status', desc: '守护进程状态、运行环境与本机 Agent' },
  { key: 'servers', label: 'MCP 管理', icon: 'servers', desc: '所有 MCP server 的统一管理：注册、启停与工具开关' },
  { key: 'discovery', label: 'MCP 发现', icon: 'discovery', desc: '浏览官方 / Smithery / npm 源，一键收录' },
  { key: 'sync', label: 'Agent 分发', icon: 'sync', desc: '分组管理，一次配置同步到 Claude Code、Cursor 等 agent' },
  { key: 'gateway', label: '聚合网关', icon: 'gateway', desc: '多个 MCP 聚合为一个端点、访问令牌与上游进程' },
  { key: 'logs', label: '调用日志', icon: 'logs', desc: '网关调用记录与上游进程输出' },
  { key: 'skills', label: 'Skill 管理', icon: 'skills', desc: '安装 / 启停 / 更新 SKILL.md 技能，分发到 8 个 agent' },
  { key: 'providers', label: '供应商', icon: 'providers', desc: 'Claude Code / Codex / Gemini 供应商一键切换' },
] as const;

export type TabKey = (typeof TABS)[number]['key'];

function initialTab(): TabKey {
  const t = new URLSearchParams(window.location.search).get('tab') as TabKey | null;
  return t !== null && TABS.some((x) => x.key === t) ? t : 'status';
}

export default function App() {
  return (
    <ToastProvider>
      <AppShell />
    </ToastProvider>
  );
}

function AppShell() {
  const [authed, setAuthed] = useState(getToken() !== '');
  const [tab, setTab] = useState<TabKey>(initialTab);
  const queryClient = useQueryClient();

  // 键盘直达：Alt+数字切页；/ 聚焦当前页搜索框（输入控件聚焦时不劫持）
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      const t = e.target as HTMLElement | null;
      if (t !== null && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) {
        return;
      }
      if (e.key === '/') {
        const search = document.querySelector<HTMLElement>('[data-search-input]');
        if (search !== null) {
          e.preventDefault();
          search.focus();
        }
        return;
      }
      if (!e.altKey || e.ctrlKey || e.metaKey) return;
      const target = TABS[Number(e.key) - 1];
      if (target !== undefined) {
        e.preventDefault();
        setTab(target.key);
        const url = new URL(window.location.href);
        url.searchParams.set('tab', target.key);
        window.history.replaceState(null, '', url);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  // 先探测令牌开关——默认关闭时直接进入，不显示令牌门
  const authInfo = useQuery({
    queryKey: ['auth-info'],
    queryFn: () => api.get<{ authRequired: boolean }>('/api/auth/info'),
    retry: false,
    staleTime: Infinity,
  });
  const authRequired = authInfo.data?.authRequired === true;

  const status = useQuery({
    queryKey: ['status'],
    queryFn: () => api.get<StatusInfo>('/api/status'),
    retry: false,
    enabled: authed || !authRequired,
    refetchInterval: 10_000,
  });

  const unauthorized = status.error instanceof ApiError && status.error.status === 401;
  const needsGate = authRequired && (!authed || unauthorized);

  const switchTab = (key: TabKey): void => {
    setTab(key);
    const url = new URL(window.location.href);
    url.searchParams.set('tab', key);
    window.history.replaceState(null, '', url);
  };

  if (authInfo.isLoading || status.isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center gap-3 text-sm text-slate-500">
        <Spinner className="h-5 w-5 text-brand-600" />
        连接 daemon…
      </div>
    );
  }
  if (needsGate) {
    return (
      <TokenGate
        onOk={() => {
          setAuthed(true);
          void queryClient.invalidateQueries();
        }}
      />
    );
  }

  if (status.isError) {
    return (
      <div className="flex min-h-screen items-center justify-center p-6">
        <div className="flex max-w-md items-start gap-3 rounded-xl border border-rose-200 bg-rose-50 p-4 text-sm leading-relaxed text-rose-700">
          <Icon name="alert" className="mt-0.5 h-5 w-5 shrink-0" />
          <div>
            无法连接 daemon（{String((status.error as Error).message)}）。请确认已运行{' '}
            <code className="rounded bg-rose-100 px-1 py-0.5 font-mono text-xs">dam start</code>。
          </div>
        </div>
      </div>
    );
  }

  const gwRunning = status.data?.gateway.running === true;
  const current = TABS.find((t) => t.key === tab) ?? TABS[0];

  return (
    <div className="flex h-screen overflow-hidden">
      <aside className="flex w-56 shrink-0 flex-col bg-slate-100">
        <div className="flex h-16 shrink-0 items-center gap-3 border-b border-slate-200 px-5">
          <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-gradient-to-br from-brand-400 to-brand-600 shadow-lg shadow-brand-500/25">
            <BrandMark className="h-5.5 w-5.5 text-white" />
          </div>
          <div className="leading-tight">
            <div className="text-sm font-semibold text-slate-900">Beaver</div>
            <div className="mt-0.5 text-xs tabular-nums text-slate-500">
              v{status.data?.version} · 端口 {status.data?.port}
            </div>
          </div>
        </div>
        <nav data-testid="sidebar" className="flex-1 space-y-1 overflow-y-auto p-3">
          {TABS.map((t, i) => {
            const active = tab === t.key;
            return (
              <button
                key={t.key}
                data-testid={`tab-${t.key}`}
                onClick={() => switchTab(t.key)}
                title={`${t.desc}（快捷键 Alt+${i + 1}）`}
                className={`group flex w-full items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors duration-150 ${
                  active ? 'bg-slate-200 text-slate-900' : 'text-slate-500 hover:bg-slate-200/60 hover:text-slate-800'
                }`}
              >
                <Icon
                  name={t.icon}
                  className={`h-4.5 w-4.5 shrink-0 transition-colors ${
                    active ? 'text-brand-600' : 'text-slate-400 group-hover:text-slate-600'
                  }`}
                />
                {t.label}
                {active && <span className="ml-auto h-1.5 w-1.5 shrink-0 rounded-full bg-brand-500" />}
              </button>
            );
          })}
        </nav>
        <div className="shrink-0 border-t border-slate-200 p-3">
          <div className="flex items-center gap-2 rounded-lg bg-slate-200/70 px-3 py-2 text-xs text-slate-500">
            <span className="relative flex h-2 w-2 shrink-0">
              {gwRunning && (
                <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-60" />
              )}
              <span className={`relative inline-flex h-2 w-2 rounded-full ${gwRunning ? 'bg-emerald-500' : 'bg-slate-400'}`} />
            </span>
            网关{gwRunning ? '运行中' : '已停止'}
          </div>
          <div className="mt-1.5 px-1 text-xs leading-relaxed text-slate-400">
            <kbd className="rounded border border-slate-300/80 bg-white px-1 py-0.5 font-mono text-slate-500">Alt+1-8</kbd> 切页 ·{' '}
            <kbd className="rounded border border-slate-300/80 bg-white px-1 py-0.5 font-mono text-slate-500">/</kbd> 搜索
          </div>
        </div>
      </aside>
      <main className="flex-1 overflow-y-auto">
        {/* 不做页面级入场动画：初帧时间线被节流的环境（无头截图/后台标签）会停在透明第一帧 */}
        <div className="mx-auto max-w-7xl px-6 py-5">
          <header className="mb-4 flex flex-wrap items-baseline gap-x-3">
            <h1 className="font-display text-xl font-medium tracking-tight text-slate-900">{current.label}</h1>
            <p className="text-xs text-slate-500">{current.desc}</p>
          </header>
          {tab === 'status' && <StatusPage onNavigate={switchTab} />}
          {tab === 'servers' && <ServersPage onNavigate={switchTab} />}
          {tab === 'discovery' && <DiscoveryPage onNavigate={switchTab} />}
          {tab === 'sync' && <SyncPage />}
          {tab === 'gateway' && <GatewayPage onNavigate={switchTab} />}
          {tab === 'logs' && <LogsPage />}
        {tab === 'skills' && <SkillsPage />}
        {tab === 'providers' && <ProvidersPage />}
        </div>
      </main>
    </div>
  );
}

function TokenGate({ onOk }: { onOk: () => void }) {
  const [value, setValue] = useState('');
  const [error, setError] = useState('');
  const tryAuth = async (): Promise<void> => {
    setToken(value.trim());
    try {
      await api.get('/api/status');
      onOk();
    } catch (err) {
      setError(`token 无效或 daemon 未运行（${String((err as Error).message)}）`);
    }
  };
  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-100">
      <div className="w-96 animate-scale-in rounded-2xl bg-white p-8 shadow-pop">
        <div className="mb-6 flex items-center gap-3">
          <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-gradient-to-br from-brand-400 to-brand-600 shadow-lg shadow-brand-500/25">
            <BrandMark className="h-6 w-6 text-white" />
          </div>
          <div>
            <h1 className="font-display text-lg font-medium tracking-tight text-slate-900">Beaver</h1>
            <p className="text-xs text-slate-400">本机守护进程访问</p>
          </div>
        </div>
        <p className="mb-4 text-xs leading-relaxed text-slate-500">
          输入访问令牌（见 <code className="rounded bg-slate-100 px-1 py-0.5 font-mono text-[11px]">~/.beaver/config.json</code>{' '}
          的 settings.token，或 <code className="rounded bg-slate-100 px-1 py-0.5 font-mono text-[11px]">dam status</code>）
        </p>
        <input
          data-testid="token-input"
          className="mb-3 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-800 shadow-sm transition placeholder:text-slate-400 hover:border-slate-400 focus:border-brand-500 focus:outline-none focus:ring-4 focus:ring-brand-500/10"
          type="password"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void tryAuth();
          }}
          placeholder="daemon access token"
        />
        {error !== '' && (
          <div className="mb-3 rounded-lg bg-rose-50 px-3 py-2 text-sm leading-relaxed text-rose-700">{error}</div>
        )}
        <button
          data-testid="token-submit"
          className="h-10 w-full rounded-lg bg-brand-500 text-sm font-medium text-white shadow-sm shadow-brand-500/25 transition-all duration-150 hover:bg-brand-600 active:bg-brand-700 active:scale-[0.98]"
          onClick={() => void tryAuth()}
        >
          进入
        </button>
      </div>
    </div>
  );
}
