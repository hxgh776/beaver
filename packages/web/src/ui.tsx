import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';

// —— 设计系统：所有交互组件覆盖 hover / active / focus / disabled 状态 ——

export function Card({
  title,
  children,
  actions,
  subtitle,
  className = '',
}: {
  title?: string;
  children: ReactNode;
  actions?: ReactNode;
  subtitle?: string;
  className?: string;
}) {
  return (
    <section className={`mb-4 rounded-xl border border-slate-200/80 bg-white p-4 shadow-card ${className}`}>
      {(title !== undefined || actions !== undefined) && (
        <div className="mb-3 flex items-start justify-between gap-3">
          <div className="min-w-0">
            {title !== undefined && (
              <h2 className="text-base font-semibold tracking-tight text-slate-900">{title}</h2>
            )}
            {subtitle !== undefined && <p className="mt-1 text-xs leading-relaxed text-slate-500">{subtitle}</p>}
          </div>
          {actions !== undefined && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  );
}

export function Btn(props: {
  children: ReactNode;
  onClick?: () => void;
  kind?: 'primary' | 'default' | 'danger';
  disabled?: boolean;
  testId?: string;
  small?: boolean;
  title?: string;
  className?: string;
}) {
  const kind =
    props.kind === 'primary'
      ? 'bg-brand-500 text-white shadow-sm shadow-brand-500/25 hover:bg-brand-600 active:bg-brand-700'
      : props.kind === 'danger'
        ? 'bg-rose-500 text-white shadow-sm shadow-rose-500/20 hover:bg-rose-600 active:bg-rose-700'
        : 'border border-slate-300 bg-white text-slate-700 shadow-sm hover:border-slate-400 hover:bg-slate-50 hover:text-slate-900';
  return (
    <button
      data-testid={props.testId}
      onClick={props.onClick}
      disabled={props.disabled}
      title={props.title}
      className={`${props.className ?? ''} inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg font-medium transition-all duration-150 active:scale-[0.97] disabled:pointer-events-none disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-500 ${
        props.small === true ? 'h-7 rounded-md px-2.5 text-xs' : 'h-10 px-4 text-sm'
      } ${kind}`}
    >
      {props.children}
    </button>
  );
}

export function Badge({ tone, children }: { tone: 'green' | 'red' | 'gray' | 'blue' | 'amber'; children: ReactNode }) {
  const map = {
    green: 'bg-emerald-50 text-emerald-700 ring-emerald-600/15',
    red: 'bg-rose-50 text-rose-700 ring-rose-600/15',
    gray: 'bg-slate-100 text-slate-600 ring-slate-500/15',
    blue: 'bg-brand-50 text-brand-700 ring-brand-600/15',
    amber: 'bg-amber-50 text-amber-800 ring-amber-600/20',
  } as const;
  return (
    <span
      className={`inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium ring-1 ring-inset ${map[tone]}`}
    >
      {children}
    </span>
  );
}

/** 可删除的小标签（chips），用于分组/绑定关系展示 */
export function Chip(props: { children: ReactNode; onRemove?: () => void; tone?: 'gray' | 'blue' }) {
  const cls =
    props.tone === 'blue'
      ? 'border-brand-200 bg-brand-50 text-brand-700'
      : 'border-slate-200 bg-slate-50 text-slate-600';
  return (
    <span className={`inline-flex max-w-full items-center gap-1 rounded-full border px-2 py-0.5 text-xs ${cls}`}>
      <span className="truncate">{props.children}</span>
      {props.onRemove !== undefined && (
        <button
          onClick={props.onRemove}
          className="-mr-0.5 shrink-0 rounded-full p-0.5 text-slate-400 transition-colors hover:bg-rose-100 hover:text-rose-600"
          title="移除"
          aria-label="移除"
        >
          ×
        </button>
      )}
    </span>
  );
}

/** 开关（原生 checkbox + 样式轨道，键盘可达） */
export function Toggle(props: {
  checked: boolean;
  onChange: (v: boolean) => void;
  testId?: string;
  disabled?: boolean;
  label?: string;
}) {
  return (
    <label className="inline-flex cursor-pointer select-none items-center gap-2.5">
      <span className="relative inline-flex h-5 w-9 shrink-0 items-center">
        <input
          type="checkbox"
          data-testid={props.testId}
          checked={props.checked}
          disabled={props.disabled}
          onChange={(e) => props.onChange(e.target.checked)}
          className="peer sr-only"
        />
        <span className="absolute inset-0 rounded-full bg-slate-300 transition-colors duration-150 peer-checked:bg-brand-500 peer-focus-visible:ring-2 peer-focus-visible:ring-brand-400 peer-focus-visible:ring-offset-2" />
        <span className="relative ml-0.5 h-4 w-4 rounded-full bg-white shadow transition-transform duration-150 peer-checked:translate-x-4" />
      </span>
      {props.label !== undefined && <span className="text-sm text-slate-600">{props.label}</span>}
    </label>
  );
}

export function Field(props: { label: string; children: ReactNode; hint?: string }) {
  return (
    <label className="mb-3 block text-sm">
      <span className="mb-1 block font-medium text-slate-700">{props.label}</span>
      {props.children}
      {props.hint !== undefined && <span className="mt-1 block text-xs leading-relaxed text-slate-400">{props.hint}</span>}
    </label>
  );
}

export const inputCls =
  'w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-800 shadow-sm transition placeholder:text-slate-400 hover:border-slate-400 focus:border-brand-500 focus:outline-none focus:ring-4 focus:ring-brand-500/10';

export const selectCls =
  'rounded-lg border border-slate-300 bg-white px-2.5 py-1.5 text-sm text-slate-700 shadow-sm transition hover:border-slate-400 focus:border-brand-500 focus:outline-none focus:ring-4 focus:ring-brand-500/10';

/** 卡片内小号下拉（添加到分组/关联 agent 等） */
export const selectSmCls =
  'w-full rounded-md border border-slate-300 bg-white px-2 py-1 text-xs text-slate-600 shadow-sm transition hover:border-slate-400 focus:border-brand-500 focus:outline-none focus:ring-4 focus:ring-brand-500/10';

/** 深色代码胶囊（端点 / token 展示）：Claude 深暖色面板 + 米白文字 */
export const codeChipCls =
  'break-all rounded-md bg-slate-950 px-2.5 py-1.5 font-mono text-xs leading-relaxed text-slate-100 ring-1 ring-white/10';

/** 表格统一样式 */
export const theadCls = 'border-b border-slate-200 text-left text-xs font-medium text-slate-400';
export const rowCls = 'border-b border-slate-100 transition-colors hover:bg-slate-50/60';

// —— 轻量 toast：操作反馈右上角弹出、自动消失——
// 替代页面顶部常驻横幅：长页面底部操作时反馈依然可见，且不会永久占据版面

export interface ToastApi {
  ok: (text: string) => void;
  err: (text: string) => void;
}

const ToastCtx = createContext<ToastApi | null>(null);

export function useToast(): ToastApi {
  const ctx = useContext(ToastCtx);
  if (ctx === null) throw new Error('useToast 必须在 <ToastProvider> 内使用');
  return ctx;
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<{ id: number; kind: 'ok' | 'err'; text: string }[]>([]);
  const nextId = useRef(0);
  const remove = useCallback((id: number) => setItems((prev) => prev.filter((t) => t.id !== id)), []);
  const push = useCallback(
    (kind: 'ok' | 'err', text: string) => {
      const id = ++nextId.current;
      // 最多同时 4 条，避免连环操作把屏幕糊满
      setItems((prev) => [...prev.slice(-3), { id, kind, text }]);
      window.setTimeout(() => remove(id), kind === 'ok' ? 4500 : 7000);
    },
    [remove],
  );
  const api = useMemo<ToastApi>(() => ({ ok: (t) => push('ok', t), err: (t) => push('err', t) }), [push]);
  return (
    <ToastCtx.Provider value={api}>
      {children}
      <div className="pointer-events-none fixed right-4 top-4 z-50 flex w-88 max-w-[calc(100vw-2rem)] flex-col gap-2">
        {items.map((t) => (
          <div
            key={t.id}
            role="status"
            className={`pointer-events-auto flex animate-fade-up items-start gap-2.5 rounded-xl border px-3.5 py-2.5 text-sm shadow-pop ${
              t.kind === 'ok' ? 'border-emerald-200 bg-emerald-50 text-emerald-800' : 'border-rose-200 bg-rose-50 text-rose-700'
            }`}
          >
            <Icon name={t.kind === 'ok' ? 'check' : 'alert'} className="mt-0.5 h-4 w-4 shrink-0" />
            <span className="min-w-0 flex-1 break-all leading-relaxed">{t.text}</span>
            <button
              onClick={() => remove(t.id)}
              className="shrink-0 rounded p-0.5 opacity-40 transition hover:opacity-90"
              aria-label="关闭"
            >
              ✕
            </button>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

// —— 复制按钮：点击后短暂显示「已复制」——
// 非安全上下文（局域网 IP 打开）时 navigator.clipboard 不存在，退回 execCommand
// label 传空串 = 纯图标模式（卡片命令行等紧凑场景）

export function CopyBtn({ text, label = '复制', className = '' }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = useState(false);
  const copy = async (): Promise<void> => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      ta.remove();
    }
    setDone(true);
    window.setTimeout(() => setDone(false), 1600);
  };
  const iconOnly = label === '';
  return (
    <button
      onClick={() => void copy()}
      title="复制到剪贴板"
      aria-label={iconOnly ? '复制' : undefined}
      className={`inline-flex h-7 shrink-0 items-center justify-center gap-1 whitespace-nowrap rounded-md border transition-all duration-150 active:scale-[0.97] ${
        iconOnly ? 'w-7' : 'px-2'
      } ${
        done
          ? 'border-emerald-300 bg-emerald-50 text-emerald-700'
          : 'border-slate-200 bg-white text-slate-500 hover:border-slate-300 hover:text-slate-800'
      } ${className}`}
    >
      {done ? (
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="h-3.5 w-3.5 shrink-0" aria-hidden>
          <path d="M20 6L9 17l-5-5" />
        </svg>
      ) : (
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" className="h-3.5 w-3.5 shrink-0" aria-hidden>
          <rect x="9" y="9" width="11" height="11" rx="2" />
          <path d="M5 15V5a2 2 0 0 1 2-2h10" />
        </svg>
      )}
      {!iconOnly && <span className="text-xs font-medium">{done ? '已复制' : label}</span>}
    </button>
  );
}

// —— 可搜索的「添加」弹出选择器：替代单向原生 select——
// 点选项即生效，支持输入筛选；默认向上弹出（页面卡片底部场景）

export function AddMenu(props: {
  label: string;
  options: Array<{ value: string; label: string; hint?: string }>;
  onPick: (value: string) => void;
  testId?: string;
  /** 选项按钮的 testid 生成器（e2e 用） */
  optionTestId?: (value: string) => string | undefined;
  direction?: 'up' | 'down';
}) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState('');
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent): void => {
      if (root.current !== null && !root.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);
  const kw = q.trim().toLowerCase();
  const filtered = kw === '' ? props.options : props.options.filter((o) => o.label.toLowerCase().includes(kw));
  const pos = props.direction === 'down' ? 'top-full mt-1.5' : 'bottom-full mb-1.5';
  return (
    <div ref={root} className="relative">
      <button
        data-testid={props.testId}
        onClick={() => {
          setOpen((v) => !v);
          setQ('');
        }}
        className="inline-flex h-7 items-center gap-1 rounded-md border border-dashed border-slate-300 px-2 text-xs font-medium text-slate-500 transition-colors duration-150 hover:border-brand-400 hover:text-brand-600"
      >
        ＋ {props.label}
      </button>
      {open && (
        <div className={`absolute left-0 z-30 flex w-64 animate-scale-in flex-col overflow-hidden rounded-xl border border-slate-200 bg-white shadow-pop ${pos}`}>
          <input
            autoFocus
            className="w-full border-b border-slate-100 px-3 py-2 text-sm text-slate-800 outline-none placeholder:text-slate-400"
            placeholder="输入筛选…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          <div className="max-h-56 overflow-y-auto py-1">
            {filtered.length === 0 ? (
              <p className="px-3 py-2 text-xs text-slate-400">没有匹配项</p>
            ) : (
              filtered.map((o) => (
                <button
                  key={o.value}
                  data-testid={props.optionTestId?.(o.value)}
                  onClick={() => {
                    props.onPick(o.value);
                    setOpen(false);
                  }}
                  className="flex w-full items-center justify-between gap-2 px-3 py-1.5 text-left text-sm text-slate-700 transition-colors hover:bg-brand-50 hover:text-brand-700"
                >
                  <span className="min-w-0 truncate">{o.label}</span>
                  {o.hint !== undefined && <span className="shrink-0 text-xs text-slate-400">{o.hint}</span>}
                </button>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  );
}

export function Spinner({ className = 'h-4 w-4' }: { className?: string }) {
  return (
    <svg className={`animate-spin ${className}`} viewBox="0 0 24 24" fill="none" aria-hidden>
      <circle cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" className="opacity-20" />
      <path d="M22 12a10 10 0 0 0-10-10" stroke="currentColor" strokeWidth="3" strokeLinecap="round" />
    </svg>
  );
}

export function ErrorBanner({ message }: { message: string | null }) {
  if (message === null || message === '') return null;
  return (
    <div
      role="alert"
      className="mb-5 flex animate-fade-in items-start gap-2.5 rounded-lg border border-rose-200 bg-rose-50 px-3.5 py-2.5 text-sm text-rose-700"
    >
      <Icon name="alert" className="mt-0.5 h-4 w-4 shrink-0" />
      <span className="min-w-0 break-all">{message}</span>
    </div>
  );
}

export function OkBanner({ message }: { message: string | null }) {
  if (message === null || message === '') return null;
  return (
    <div className="mb-5 flex animate-fade-in items-start gap-2.5 rounded-lg border border-emerald-200 bg-emerald-50 px-3.5 py-2.5 text-sm text-emerald-700">
      <Icon name="check" className="mt-0.5 h-4 w-4 shrink-0" />
      <span className="min-w-0 break-all">{message}</span>
    </div>
  );
}

// —— 弹窗 ——

export function Modal(props: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') props.onClose();
    };
    window.addEventListener('keydown', onKey);
    // 打开弹窗自动聚焦第一个输入控件，省一次点击；无输入控件的确认框无副作用
    panel.current?.querySelector<HTMLElement>('input, select, textarea')?.focus();
    return () => window.removeEventListener('keydown', onKey);
  }, [props.onClose]);
  return (
    <div
      className="fixed inset-0 z-20 flex animate-fade-in items-center justify-center bg-slate-950/30 p-4 backdrop-blur-[2px]"
      onClick={props.onClose}
    >
      <div
        ref={panel}
        className={`flex max-h-[85vh] w-full animate-scale-in flex-col rounded-2xl bg-white shadow-pop ${
          props.wide === true ? 'max-w-2xl' : 'max-w-lg'
        }`}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-slate-100 px-5 py-4">
          <h3 className="text-base font-semibold tracking-tight text-slate-900">{props.title}</h3>
          <button
            onClick={props.onClose}
            className="flex h-8 w-8 items-center justify-center rounded-lg text-slate-400 transition-colors hover:bg-slate-100 hover:text-slate-600"
            aria-label="关闭"
          >
            ✕
          </button>
        </div>
        <div className="overflow-y-auto px-5 py-5">{props.children}</div>
        {props.footer !== undefined && (
          <div className="flex justify-end gap-2 rounded-b-2xl border-t border-slate-100 bg-slate-50/60 px-5 py-3.5">
            {props.footer}
          </div>
        )}
      </div>
    </div>
  );
}

export function ConfirmDialog(props: {
  title: string;
  message: string;
  confirmText?: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <Modal
      title={props.title}
      onClose={props.onCancel}
      footer={
        <>
          <Btn onClick={props.onCancel}>取消</Btn>
          <Btn kind="danger" onClick={props.onConfirm}>
            {props.confirmText ?? '删除'}
          </Btn>
        </>
      }
    >
      <p className="text-sm leading-relaxed text-slate-600">{props.message}</p>
    </Modal>
  );
}

// —— 分页 ——

export function Pagination(props: {
  page: number;
  hasMore: boolean;
  total?: number;
  totalPages?: number;
  pageSize: number;
  onPage: (p: number) => void;
}) {
  const start = props.page * props.pageSize;
  const rangeText =
    props.total !== undefined && props.total > 0
      ? props.totalPages !== undefined && props.totalPages > 0
        ? `共 ${props.total} 个 · 第 ${props.page + 1} / ${props.totalPages} 页`
        : `第 ${start + 1}-${Math.min(start + props.pageSize, props.total)} 条 · 共 ${props.total} 条`
      : `第 ${props.page + 1} 页${props.hasMore ? '（已显示部分，可继续翻页）' : ''}`;
  return (
    <div className="mt-3 flex items-center justify-between border-t border-slate-100 pt-2.5">
      <span className="text-xs tabular-nums text-slate-400">{rangeText}</span>
      <div className="flex gap-2">
        <Btn small disabled={props.page === 0} onClick={() => props.onPage(props.page - 1)}>
          上一页
        </Btn>
        <Btn small disabled={!props.hasMore} onClick={() => props.onPage(props.page + 1)}>
          下一页
        </Btn>
      </div>
    </div>
  );
}

/** 数字分页（带 … 省略号）：固定集合的分页切换，放在卡片头部；仅一页时不渲染 */
export function Pager(props: {
  page: number;
  totalPages: number;
  onPage: (p: number) => void;
  className?: string;
}) {
  const { page, totalPages } = props;
  if (totalPages <= 1) return null;
  // 页码序列：两端页码常驻，中间窗口对齐当前页，超出的用 … 折叠
  const seq: Array<number | '…'> = [];
  if (totalPages <= 7) {
    for (let i = 0; i < totalPages; i++) seq.push(i);
  } else {
    seq.push(0);
    const lo = Math.max(1, page - 1);
    const hi = Math.min(totalPages - 2, page + 1);
    if (lo > 1) seq.push('…');
    for (let i = lo; i <= hi; i++) seq.push(i);
    if (hi < totalPages - 2) seq.push('…');
    seq.push(totalPages - 1);
  }
  const cls = (active: boolean): string =>
    `inline-flex h-7 min-w-7 items-center justify-center rounded-md px-1.5 text-xs font-medium tabular-nums transition-all duration-150 active:scale-[0.95] focus-visible:outline-2 focus-visible:outline-brand-500 ${
      active ? 'bg-brand-500 text-white shadow-sm shadow-brand-500/25' : 'text-slate-500 hover:bg-slate-200/70 hover:text-slate-800'
    }`;
  return (
    <nav className={`flex items-center gap-0.5 ${props.className ?? ''}`} aria-label="分页">
      <button type="button" className={cls(false)} disabled={page === 0} onClick={() => props.onPage(page - 1)} aria-label="上一页">
        ‹
      </button>
      {seq.map((p, i) =>
        p === '…' ? (
          <span key={`ellipsis-${i}`} className="px-0.5 text-xs text-slate-400">
            …
          </span>
        ) : (
          <button key={p} type="button" className={cls(p === page)} aria-current={p === page ? 'page' : undefined} onClick={() => props.onPage(p)}>
            {p + 1}
          </button>
        ),
      )}
      <button type="button" className={cls(false)} disabled={page === totalPages - 1} onClick={() => props.onPage(page + 1)} aria-label="下一页">
        ›
      </button>
    </nav>
  );
}

// —— 分段标签（胶囊 tab 条）：卡片内切换内容视图 ——

export function Segmented<T extends string>(props: {
  value: T;
  onChange: (v: T) => void;
  options: Array<{ value: T; label: ReactNode; testId?: string; disabled?: boolean; title?: string }>;
  className?: string;
}) {
  return (
    <div className={`flex gap-1 rounded-lg bg-slate-100 p-1 ${props.className ?? ''}`}>
      {props.options.map((o) => (
        <button
          key={o.value}
          type="button"
          data-testid={o.testId}
          disabled={o.disabled}
          title={o.title}
          onClick={() => props.onChange(o.value)}
          className={`rounded-md px-3 py-1 text-xs font-medium transition-colors duration-150 disabled:cursor-not-allowed disabled:opacity-40 ${
            props.value === o.value ? 'bg-white text-brand-700 shadow-sm' : 'text-slate-500 hover:text-slate-700'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

// —— 图标（内联 SVG，线性风格） ——

const ICON_PATHS: Record<string, ReactNode> = {
  status: <path d="M3 12h4l3 8 4-16 3 8h4" />,
  servers: (
    <>
      <rect x="3" y="4" width="18" height="7" rx="1.5" />
      <rect x="3" y="13" width="18" height="7" rx="1.5" />
      <path d="M7 7.5h.01M7 16.5h.01" />
    </>
  ),
  discovery: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M15.5 8.5l-2 5-5 2 2-5z" />
    </>
  ),
  sync: (
    <>
      <circle cx="6" cy="12" r="2.5" />
      <circle cx="17" cy="5.5" r="2.5" />
      <circle cx="17" cy="18.5" r="2.5" />
      <path d="M8.3 10.8l6.4-4M8.3 13.2l6.4 4" />
    </>
  ),
  gateway: (
    <>
      <rect x="9" y="3" width="6" height="6" rx="1" />
      <rect x="3" y="15" width="6" height="6" rx="1" />
      <rect x="15" y="15" width="6" height="6" rx="1" />
      <path d="M12 9v3m-6 3v-3h12v3" />
    </>
  ),
  logs: (
    <>
      <rect x="5" y="3" width="14" height="18" rx="1.5" />
      <path d="M8.5 8h7M8.5 12h7M8.5 16h4" />
    </>
  ),
  skills: (
    <>
      <path d="M12 3l2.4 4.9 5.4.8-3.9 3.8.9 5.4-4.8-2.5-4.8 2.5.9-5.4L4.2 8.7l5.4-.8z" />
    </>
  ),
  providers: (
    <>
      <path d="M4 17l6-5-6-5" />
      <path d="M12 19h8" />
      <circle cx="6" cy="7" r="1.2" />
    </>
  ),
  alert: (
    <>
      <path d="M10.3 3.9L1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" />
      <path d="M12 9v4.5" />
      <path d="M12 16.8h.01" />
    </>
  ),
  check: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M8.5 12.5l2.5 2.5 4.5-5" />
    </>
  ),
};

export function Icon({ name, className = 'h-4.5 w-4.5' }: { name: keyof typeof ICON_PATHS | string; className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      aria-hidden
    >
      {ICON_PATHS[name] ?? null}
    </svg>
  );
}

/** 时间显示：ISO → 本地时区（修复调用日志时区偏移问题） */
export function formatLocalTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  const ss = String(d.getSeconds()).padStart(2, '0');
  const ms = String(d.getMilliseconds()).padStart(3, '0');
  return `${hh}:${mm}:${ss}.${ms}`;
}

export function formatLocalDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString('zh-CN', { hour12: false });
}

/** 品牌 Logo：河狸剪影（圆耳 + 大脸 + 大板牙），颜色跟随 currentColor */
export function BrandMark({ className = 'h-5 w-5' }: { className?: string }) {
  return (
    <svg viewBox="0 0 64 64" fill="none" className={className} aria-hidden="true">
      <g fill="currentColor">
        <circle cx="15.5" cy="16.5" r="5.6" />
        <circle cx="48.5" cy="16.5" r="5.6" />
        <path
          fillRule="evenodd"
          d="M10.5 35 a21.5 18 0 1 0 43 0 a21.5 18 0 1 0 -43 0 Z M21.3 31.5 a2.7 2.7 0 1 0 5.4 0 a2.7 2.7 0 1 0 -5.4 0 Z M37.3 31.5 a2.7 2.7 0 1 0 5.4 0 a2.7 2.7 0 1 0 -5.4 0 Z"
        />
        <rect x="27.6" y="43.5" width="4.1" height="11.5" rx="1.7" />
        <rect x="32.3" y="43.5" width="4.1" height="11.5" rx="1.7" />
      </g>
    </svg>
  );
}
