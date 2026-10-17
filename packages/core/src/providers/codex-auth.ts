
/**
 * Codex auth.json 四去向计划（自 cc-switch codex_login.rs:212-321 的 plan() 移植，
 * 裁剪掉托管账号/代理分支——v1 只做第三方切换与官方 API-Key/OAuth 卡）。
 * 四去向：删 + 暂存 / 整份写行里的 auth / 托管写 / 不动。
 */

/** 暂存文件结构（~/.beaver/codex-login-stash.json，0600 语义由数据目录权限兜底） */
export interface LoginStash {
  logins: Record<string, unknown>;
  /** 最近一次暂存的身份：切回没有自己登录的官方卡时还它 */
  last?: string;
  version: number;
}

export const STASH_VERSION = 1;

// —— 凭据材质判定（codex_config.rs 移植） ——

export function extractApiKey(auth: Record<string, unknown> | null): string | null {
  const v = auth?.['OPENAI_API_KEY'];
  if (typeof v !== 'string') return null;
  const t = v.trim();
  return t === '' ? null : t;
}

function valuePresent(v: unknown): boolean {
  if (v === null || v === undefined) return false;
  if (typeof v === 'string') return v.trim() !== '';
  if (Array.isArray(v)) return v.length > 0;
  if (typeof v === 'object') return Object.keys(v).length > 0;
  return true;
}

/** Codex 会拿来登录的东西（OAuth token / API Key 等），元数据不算 */
export function hasLoginMaterial(auth: Record<string, unknown> | null): boolean {
  if (!auth) return false;
  return Object.entries(auth).some(([key, value]) => {
    if (key === 'auth_mode') return false;
    if (key === 'OPENAI_API_KEY') return typeof value === 'string' && value.trim() !== '';
    return valuePresent(value);
  });
}

/** 纯 OAuth/凭据登录（id_token/tokens 等）；单独的 OPENAI_API_KEY 不算 */
export function hasCredentialLoginMaterial(auth: Record<string, unknown> | null): boolean {
  if (!auth) return false;
  if (['personal_access_token', 'agent_identity', 'bedrock_api_key'].some((k) => valuePresent(auth[k]))) {
    return true;
  }
  const tokens = auth['tokens'];
  return tokens !== null && typeof tokens === 'object' && Object.keys(tokens as object).length > 0;
}

/** login_on_disk 判定：写完 auth.json 后 Codex 是否有登录（决定 requires_openai_auth） */
export function hasOpenaiAccountMaterial(auth: Record<string, unknown> | null): boolean {
  if (!auth) return false;
  if (extractApiKey(auth) !== null) return true;
  if (valuePresent(auth['personal_access_token']) || valuePresent(auth['agent_identity'])) return true;
  const tokens = auth['tokens'];
  if (tokens !== null && typeof tokens === 'object') {
    const t = tokens as Record<string, unknown>;
    return ['id_token', 'access_token', 'refresh_token'].some((k) => valuePresent(t[k]));
  }
  return false;
}

// —— 身份识别 ——

function decodeJwtSegment(seg: string): Record<string, unknown> | null {
  try {
    const json = Buffer.from(seg.replace(/-/g, '+').replace(/_/g, '/'), 'base64').toString('utf8');
    const parsed: unknown = JSON.parse(json);
    return parsed !== null && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

/** id_token (JWS) → sub 身份；头/载荷/签名三段，alg 必须存在 */
export function idTokenSubject(idToken: string): string | null {
  const segs = idToken.split('.');
  if (segs.length !== 3) return null;
  const header = decodeJwtSegment(segs[0]!);
  if (!header || typeof header['alg'] !== 'string' || header['alg'].trim() === '') return null;
  const claims = decodeJwtSegment(segs[1]!);
  const sub = claims?.['sub'];
  return typeof sub === 'string' && sub.trim() !== '' ? `sub:${sub.trim()}` : null;
}

function userIdentity(auth: Record<string, unknown> | null): string | null {
  const tokens = auth?.['tokens'];
  const idToken =
    tokens !== null && typeof tokens === 'object' ? (tokens as Record<string, unknown>)['id_token'] : undefined;
  if (typeof idToken !== 'string') return null;
  return idTokenSubject(idToken);
}

/** 登录身份：id_token 的用户 + workspace account_id；API Key 登录共用一个位置 */
export function identity(auth: Record<string, unknown> | null): string {
  const user = userIdentity(auth);
  const account = (auth?.['tokens'] as Record<string, unknown> | undefined)?.['account_id'];
  const acct = typeof account === 'string' && account.trim() !== '' ? account.trim() : null;
  if (user && acct) return `${user}|account:${acct}`;
  if (user) return user;
  if (acct) return `account:${acct}`;
  if (extractApiKey(auth) !== null) return 'api-key';
  return 'unknown';
}

const oauthIdentity = (auth: Record<string, unknown> | null): string | null =>
  hasCredentialLoginMaterial(auth) ? identity(auth) : null;

// —— 计划 ——

export interface AuthInput {
  /** live auth.json（文件不存在为 null；解析失败按非对象处理） */
  live: Record<string, unknown> | null;
  /** 第三方供应商行里的 Key（识别旧版残留） */
  thirdPartyKeys: string[];
  /** 目标：第三方（preserve = 保留登录开关）或官方卡（rowAuth 为其行内 auth） */
  target:
    | { kind: 'third-party'; preserve: boolean }
    | { kind: 'official'; rowAuth: Record<string, unknown> | null };
}

export interface AuthPlan {
  /** null 不动；'delete' 删掉；{auth} 整份写 */
  auth: 'keep' | 'delete' | { auth: Record<string, unknown> };
  /** 暂存有变化时的新内容（null = 不写） */
  stash: LoginStash | null;
  /** 写完之后 auth.json 里有没有登录（第三方路由的 requires_openai_auth 跟着它） */
  loginOnDisk: boolean;
}

export function planAuth(input: AuthInput): AuthPlan {
  const live = input.live;
  const native = live !== null && hasLoginMaterial(live) ? live : null;
  let stash: LoginStash = { logins: {}, version: STASH_VERSION };
  let stashDirty = false;

  const put = (auth: Record<string, unknown>): void => {
    const id = identity(auth);
    stash = { ...stash, logins: { ...stash.logins, [id]: auth }, last: id, version: STASH_VERSION };
    stashDirty = true;
  };
  const take = (id: string): Record<string, unknown> | null => {
    const auth = stash.logins[id];
    if (auth === undefined) return null;
    const logins = { ...stash.logins };
    delete logins[id];
    stash = { ...stash, logins, last: stash.last === id ? undefined : stash.last, version: STASH_VERSION };
    stashDirty = true;
    return auth as Record<string, unknown>;
  };

  let auth: AuthPlan['auth'];
  if (input.target.kind === 'third-party') {
    const { preserve } = input.target;
    if (native !== null && !preserve) put(native); // 原生登录先进暂存
    // 保留登录关闭时第三方路由旁不留任何 auth.json（写 {} 不等于登出）
    if (live !== null && !preserve) {
      auth = 'delete';
    } else {
      auth = 'keep'; // preserve 时不动 / live 本来就不存在
    }
  } else {
    // 官方卡
    const rowAuth = input.target.rowAuth;
    if (rowAuth !== null && extractApiKey(rowAuth) !== null && !hasCredentialLoginMaterial(rowAuth)) {
      // API-Key 凭据：静态不过期，照写；当前原生登录若有差异先暂存
      if (native !== null && identity(native) !== identity(rowAuth)) put(native);
      auth = { auth: rowAuth };
    } else {
      const wanted = rowAuth === null ? null : oauthIdentity(rowAuth);
      if (native !== null) {
        if (wanted !== null && identity(native) !== wanted) {
          const saved = take(wanted);
          if (saved !== null) {
            put(native);
            auth = { auth: saved };
          } else {
            auth = 'keep'; // 暂存里没有要换的人：不动（行快照可能早已作废）
          }
        } else {
          auth = 'keep'; // 行里没存登录：跟随 Codex 当前登录
        }
      } else {
        const restored = wanted !== null ? take(wanted) : stash.last ? take(stash.last) : null;
        auth = restored !== null ? { auth: restored } : 'keep'; // 无从恢复：不动（不制造登出）
      }
    }
  }

  const loginOnDisk =
    auth === 'keep'
      ? native !== null && hasOpenaiAccountMaterial(native)
      : auth === 'delete'
        ? false
        : hasOpenaiAccountMaterial(auth.auth);

  return { auth, stash: stashDirty ? stash : null, loginOnDisk };
}

/** 判断给定的 live auth 是否是某第三方 Key 的旧版残留（只删能证明是我们写的） */
export function isThirdPartyResidue(live: Record<string, unknown> | null, thirdPartyKeys: string[]): boolean {
  if (live === null || hasCredentialLoginMaterial(live)) return false;
  const key = extractApiKey(live);
  return key !== null && thirdPartyKeys.includes(key);
}
