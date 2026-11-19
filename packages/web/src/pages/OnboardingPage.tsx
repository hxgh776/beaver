import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import type { AgentInfo, ImportGroup, SyncReport } from '../api';
import { Badge, BrandMark, Btn, Card, Spinner, useToast } from '../ui';

/** M4 首启向导（docs/desktop-plan.md §6）：
 * 探测（确认识别的 agent）→ 导入（复用 import preview）→ 同步（直连/网关默认策略说明）。
 * 「跳过，直接进面板」全程可见，不绑架老手；每步底部固定安全说明（写前自动备份、可一键恢复）。 */

const STEPS = ['探测', '导入', '同步'] as const;

export function OnboardingPage({ onDone }: { onDone: () => void }) {
  const [step, setStep] = useState(0);
  const [importedCount, setImportedCount] = useState<number | null>(null);
  const finish = useMutation({
    mutationFn: () => api.patch('/api/settings', { hasOnboarded: true }),
    onSuccess: onDone,
  });
  const skip = (): void => finish.mutate();

  return (
    <div className="flex min-h-screen items-start justify-center bg-slate-100 p-6">
      <div className="w-full max-w-3xl">
        <div className="mb-5 flex items-center gap-3">
          <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-gradient-to-br from-brand-400 to-brand-600 shadow-lg shadow-brand-500/25">
            <BrandMark className="h-6 w-6 text-white" />
          </div>
          <div>
            <h1 className="font-display text-lg font-medium tracking-tight text-slate-900">欢迎使用 Beaver</h1>
            <p className="text-xs text-slate-400">三步接入本机已有的 agent 与 MCP server</p>
          </div>
        </div>

        {/* 步骤指示 */}
        <ol className="mb-4 flex items-center gap-2 text-xs">
          {STEPS.map((label, i) => (
            <li key={label} className="flex items-center gap-2">
              <span
                className={`flex h-6 w-6 items-center justify-center rounded-full font-medium ${
                  i < step
                    ? 'bg-brand-500 text-white'
                    : i === step
                      ? 'bg-brand-100 font-semibold text-brand-700 ring-1 ring-brand-400'
                      : 'bg-slate-200 text-slate-400'
                }`}
              >
                {i + 1}
              </span>
              <span className={i === step ? 'font-medium text-slate-800' : 'text-slate-400'}>{label}</span>
              {i < STEPS.length - 1 && <span className="mx-1 h-px w-8 bg-slate-300" />}
            </li>
          ))}
        </ol>

        {step === 0 && <StepDetect onNext={() => setStep(1)} />}
        {step === 1 && (
          <StepImport
            onNext={(n) => {
              setImportedCount(n);
              setStep(2);
            }}
          />
        )}
        {step === 2 && <StepSync importedCount={importedCount} onDone={() => finish.mutate()} />}

        <div className="mt-4 flex items-center justify-between">
          <p className="text-xs leading-relaxed text-slate-400">
            写入 agent 配置前自动滚动备份；改坏了随时在「设置 → 恢复备份」一键回滚。
          </p>
          <button
            data-testid="wizard-skip"
            className="shrink-0 text-xs text-slate-400 underline-offset-2 hover:text-slate-600 hover:underline"
            onClick={skip}
          >
            跳过，直接进面板
          </button>
        </div>
      </div>
    </div>
  );
}

// —— 第 1 步：探测 ——//

function StepDetect({ onNext }: { onNext: () => void }) {
  const toast = useToast();
  const agents = useQuery({
    queryKey: ['agents'],
    queryFn: () => api.get<{ agents: AgentInfo[] }>('/api/agents?refresh=1'),
  });
  const rerun = useMutation({
    mutationFn: () => agents.refetch(),
    onSuccess: (res) => {
      const list = res.data?.agents ?? [];
      toast.ok(`探测完成：${list.filter((a) => a.detected).length} 个已安装`);
    },
  });
  const list = agents.data?.agents ?? [];
  const detected = list.filter((a) => a.detected);
  return (
    <Card title="本机 agent 探测" subtitle="候选配置文件存在即视为已安装；没有也没关系，后续可随时安装">
      {agents.isLoading ? (
        <div className="flex items-center gap-2 py-6 text-sm text-slate-500">
          <Spinner /> 探测中…
        </div>
      ) : (
        <>
          <div className="mb-3 text-sm text-slate-600">
            发现 <span className="font-semibold text-brand-600">{detected.length}</span> / {list.length} 个 agent
            {detected.length > 0 && <>：{detected.map((a) => a.displayName).join('、')}</>}
          </div>
          <div className="grid max-h-56 grid-cols-2 gap-2 overflow-y-auto md:grid-cols-3">
            {list.map((a) => (
              <div key={a.agentType} className="flex items-center justify-between rounded-lg border border-slate-200 px-3 py-2 text-sm">
                <span className="truncate text-slate-700">{a.displayName}</span>
                {a.detected ? <Badge tone="green">已安装</Badge> : <Badge tone="gray">未发现</Badge>}
              </div>
            ))}
          </div>
          <div className="mt-4 flex justify-between">
            <Btn onClick={() => rerun.mutate()}>重新探测</Btn>
            <Btn kind="primary" testId="wizard-next-1" onClick={onNext}>
              下一步：导入现有 server
            </Btn>
          </div>
        </>
      )}
    </Card>
  );
}

// —— 第 2 步：导入 ——//

function StepImport({ onNext }: { onNext: (imported: number) => void }) {
  const toast = useToast();
  const qc = useQueryClient();
  const [selected, setSelected] = useState<Record<string, boolean>>({});
  const imports = useQuery({
    queryKey: ['import-preview'],
    queryFn: () => api.get<{ groups: ImportGroup[] }>('/api/import/preview'),
  });
  const doImport = useMutation({
    mutationFn: (fingerprints: string[]) => api.post<{ added: unknown[] }>('/api/import', { fingerprints }),
    onSuccess: (res) => {
      toast.ok(`已导入 ${res.added.length} 个 server`);
      void qc.invalidateQueries();
      onNext(res.added.length);
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });
  const pending = (imports.data?.groups ?? []).filter((g) => !g.alreadyImported);
  const checked = pending.filter((g) => selected[g.fingerprint] !== false);
  return (
    <Card title="导入现有 MCP server" subtitle="从已检测到的 agent 配置中读取；跨 agent 同一 server 自动按指纹去重">
      {imports.isLoading ? (
        <div className="flex items-center gap-2 py-6 text-sm text-slate-500">
          <Spinner /> 读取中…
        </div>
      ) : pending.length === 0 ? (
        <p className="py-4 text-sm text-slate-500">
          没有发现可导入的 server。没关系，之后可在「MCP 发现」页从九个源收录。
        </p>
      ) : (
        <div className="max-h-64 space-y-1.5 overflow-y-auto">
          {pending.map((g) => {
            const checkedItem = selected[g.fingerprint] !== false;
            return (
              <label
                key={g.fingerprint}
                className="flex cursor-pointer items-center gap-3 rounded-lg border border-slate-200 px-3 py-2 text-sm hover:bg-slate-50"
              >
                <input
                  type="checkbox"
                  checked={checkedItem}
                  onChange={(e) => setSelected((m) => ({ ...m, [g.fingerprint]: e.target.checked }))}
                  className="h-4 w-4"
                />
                <span className="flex-1 truncate font-medium text-slate-700">{g.suggested.name}</span>
                <span className="text-xs text-slate-400">
                  来自 {g.candidates.map((c) => c.agentType).join('、')}（{g.candidates.length} 处）
                </span>
              </label>
            );
          })}
        </div>
      )}
      <div className="mt-4 flex justify-between">
        <Btn onClick={() => onNext(0)}>跳过导入</Btn>
        <Btn
          kind="primary"
          testId="wizard-import"
          disabled={pending.length === 0 || checked.length === 0 || doImport.isPending}
          onClick={() => doImport.mutate(checked.map((g) => g.fingerprint))}
        >
          导入选中的 {checked.length} 个
        </Btn>
      </div>
    </Card>
  );
}

// —— 第 3 步：同步 ——//

function StepSync({ importedCount, onDone }: { importedCount: number | null; onDone: () => void }) {
  const toast = useToast();
  const [report, setReport] = useState<SyncReport | null>(null);
  const sync = useMutation({
    mutationFn: (dryRun: boolean) => api.post<{ report: SyncReport }>('/api/sync', { dryRun }),
    onSuccess: (res, dryRun) => {
      if (dryRun) {
        setReport(res.report);
        toast.ok('预览完成');
      } else {
        toast.ok('同步完成');
        onDone();
      }
    },
    onError: (e) => toast.err(String((e as Error).message)),
  });
  const changes = (report?.perAgent ?? []).flatMap((a) => a.changes.filter((c) => c.action !== 'none'));
  return (
    <Card title="分发到各 agent" subtitle="两种形态二选一：直连（每个 agent 写入完整 server 列表）或网关（agent 只见一个聚合端点）">
      <div className="mb-3 space-y-1.5 text-sm leading-relaxed text-slate-600">
        <p>
          {importedCount !== null && importedCount > 0
            ? `已导入 ${importedCount} 个 server，现在可以把它们分发到检测到的 agent。`
            : '注册表为空也没关系，之后添加 server 后在「Agent 分发」页同步。'}
        </p>
        <p className="text-xs text-slate-400">
          推荐先用「预览」看变更清单——dry-run 不落盘；正式同步前同样有自动备份兜底。
        </p>
      </div>
      {report !== null && (
        <div className="mb-3 max-h-40 overflow-y-auto rounded-lg bg-slate-50 p-3 text-xs text-slate-600">
          {changes.length === 0 ? (
            <p>（无待变更项）</p>
          ) : (
            changes.map((c, i) => (
              <div key={i} className="font-mono">
                {c.action.padEnd(7)} {c.key}
              </div>
            ))
          )}
        </div>
      )}
      <div className="flex justify-between">
        <Btn testId="wizard-dry-run" disabled={sync.isPending} onClick={() => sync.mutate(true)}>
          预览变更
        </Btn>
        <div className="flex gap-2">
          <Btn onClick={onDone}>以后再同步</Btn>
          <Btn kind="primary" testId="wizard-sync" disabled={sync.isPending} onClick={() => sync.mutate(false)}>
            立即同步
          </Btn>
        </div>
      </div>
    </Card>
  );
}
