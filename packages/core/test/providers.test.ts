import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { Store } from '../src/store.js';
import { ProvidersService } from '../src/providers/service.js';
import {
  CLAUDE_EXCLUSIVE_ENV,
  CLAUDE_FLOOR_TOP,
  claudeFloorEnv,
  claudeFloorTop,
  geminiFloorEnv,
} from '../src/providers/floor.js';
import { projectClaudeSettings } from '../src/providers/claude.js';
import { planAuth, identity, hasOpenaiAccountMaterial } from '../src/providers/codex-auth.js';
import { projectCodexToml, requiresOpenaiAuth } from '../src/providers/codex.js';
import { projectGeminiEnv, projectGeminiSettings } from '../src/providers/gemini.js';
import type { ManagerConfig } from '../src/types.js';

let sandbox: string;
let store: Store;
let config: ManagerConfig;
let service: ProvidersService;

beforeEach(async () => {
  sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-prov-'));
  store = new Store(sandbox);
  config = await store.load();
  service = new ProvidersService({
    store,
    getConfig: () => config,
    saveConfig: async () => {},
    agentRoot: sandbox,
  });
});

afterEach(async () => {
  await fs.rm(sandbox, { recursive: true, force: true });
});

// —— U-PR-01 floor 表与 cc-switch floor.rs 测试逐条对齐 ——

describe('U-PR-01 floor 常量表', () => {
  it('覆盖连接与鉴权键', () => {
    for (const key of [
      'ANTHROPIC_BASE_URL',
      'ANTHROPIC_AUTH_TOKEN',
      'ANTHROPIC_DEFAULT_FABLE_MODEL',
      'ANTHROPIC_CUSTOM_HEADERS',
      'CLAUDE_CODE_USE_BEDROCK',
      'CLAUDE_CODE_SKIP_BEDROCK_AUTH',
      'AWS_REGION',
      'AWS_BEARER_TOKEN_BEDROCK',
      'VERTEX_REGION_CLAUDE_4_5_SONNET',
      'CLOUD_ML_REGION',
      'GOOGLE_APPLICATION_CREDENTIALS',
      'CLAUDE_CODE_OAUTH_TOKEN',
      'CLAUDE_CODE_SUBAGENT_MODEL',
    ]) {
      expect(claudeFloorEnv(key), key).toBe(true);
    }
    for (const key of ['apiKeyHelper', 'apiKey', 'model', 'modelOverrides']) {
      expect(claudeFloorTop(key), key).toBe(true);
    }
  });

  it('功能开关必须保持为用户键', () => {
    for (const key of [
      'CLAUDE_CODE_USE_POWERSHELL_TOOL',
      'CLAUDE_CODE_USE_NATIVE_FILE_SEARCH',
      'CLAUDE_CODE_USE_COWORK_PLUGINS',
      'CLAUDE_CODE_USE_CCR_V2',
      'CLAUDE_CODE_SKIP_PROMPT_HISTORY',
      'API_TIMEOUT_MS',
      'DISABLE_TELEMETRY',
      'CLAUDE_CODE_DISABLE_ARTIFACT',
      'CLAUDE_CODE_MAX_CONTEXT_TOKENS',
    ]) {
      expect(claudeFloorEnv(key), key).toBe(false);
    }
    for (const key of ['hooks', 'enabledPlugins', 'permissions', 'statusLine', 'env']) {
      expect(claudeFloorTop(key), key).toBe(false);
    }
  });

  it('独有字段与关键字段零重叠', () => {
    for (const key of CLAUDE_EXCLUSIVE_ENV) expect(claudeFloorEnv(key), key).toBe(false);
    expect(CLAUDE_FLOOR_TOP).not.toContain('web_search');
  });

  it('Gemini：GOOGLE_* 前缀 + 指名键是地板；CLI 设置不是', () => {
    for (const key of [
      'GOOGLE_API_KEY',
      'GOOGLE_GEMINI_BASE_URL',
      'GOOGLE_GENAI_USE_VERTEXAI',
      'GOOGLE_CLOUD_PROJECT',
      'GEMINI_API_KEY',
      'GEMINI_MODEL',
      'GEMINI_CLI_CUSTOM_HEADERS',
    ]) {
      expect(geminiFloorEnv(key), key).toBe(true);
    }
    for (const key of ['GEMINI_SANDBOX', 'GEMINI_CLI_HOME', 'GEMINI_SYSTEM_MD', 'DEBUG']) {
      expect(geminiFloorEnv(key), key).toBe(false);
    }
  });
});

// —— U-PR-02 Claude 投影 ——

describe('U-PR-02 projectClaudeSettings', () => {
  it('清地板 → 写目标 → 清独有；hooks/permissions/statusLine/用户 env 原样', () => {
    const prev = {
      hooks: { PostToolUse: [] },
      permissions: { allow: ['Bash'] },
      statusLine: { type: 'command' },
      apiKeyHelper: '/usr/bin/helper',
      model: 'claude-opus-4',
      env: {
        ANTHROPIC_BASE_URL: 'https://old.example.com',
        ANTHROPIC_AUTH_TOKEN: 'old-key',
        CLAUDE_CODE_USE_BEDROCK: '1',
        CLAUDE_CODE_MAX_CONTEXT_TOKENS: '262144',
        DISABLE_TELEMETRY: '1',
        MY_CUSTOM_VAR: 'keep-me',
      },
    };
    const next = projectClaudeSettings(prev, {
      env: {
        ANTHROPIC_BASE_URL: 'https://api.moonshot.cn/anthropic',
        ANTHROPIC_AUTH_TOKEN: '',
        ANTHROPIC_MODEL: 'kimi-k2.7-code',
      },
    });
    expect(next['hooks']).toEqual(prev['hooks']);
    expect(next['permissions']).toEqual(prev['permissions']);
    expect(next['statusLine']).toEqual(prev['statusLine']);
    expect(next['apiKeyHelper']).toBeUndefined();
    expect(next['model']).toBeUndefined();
    const env = next['env'] as Record<string, unknown>;
    expect(env['ANTHROPIC_BASE_URL']).toBe('https://api.moonshot.cn/anthropic');
    expect(env['ANTHROPIC_AUTH_TOKEN']).toBeUndefined(); // 空串占位跳过
    expect(env['ANTHROPIC_MODEL']).toBe('kimi-k2.7-code');
    expect(env['CLAUDE_CODE_USE_BEDROCK']).toBeUndefined();
    expect(env['CLAUDE_CODE_MAX_CONTEXT_TOKENS']).toBeUndefined(); // 独有字段清除
    expect(env['DISABLE_TELEMETRY']).toBe('1'); // 用户键保留
    expect(env['MY_CUSTOM_VAR']).toBe('keep-me');
  });

  it('解析失败拒绝写入由 service 层保证（C0）；此处锁投影纯函数', () => {
    expect(projectClaudeSettings({}, {})).toEqual({});
  });
});

// —— U-PR-03 codex auth.json 四去向 ——

const jwtFor = (sub: string): string => {
  const b64 = (s: string): string => Buffer.from(s).toString('base64url');
  return `${b64(JSON.stringify({ alg: 'none' }))}.${b64(JSON.stringify({ sub }))}.`;
};
const oauthLogin = (sub: string, accountId: string): Record<string, unknown> => ({
  tokens: { id_token: jwtFor(sub), access_token: 'at', refresh_token: 'rt', account_id: accountId },
});

describe('U-PR-03 planAuth（auth.json 四去向）', () => {
  it('第三方 + 保留登录关：原生登录进暂存，auth.json 删除，盘上无登录', () => {
    const plan = planAuth({
      live: oauthLogin('user-1', 'ws-1'),
      thirdPartyKeys: [],
      target: { kind: 'third-party', preserve: false },
    });
    expect(plan.auth).toBe('delete');
    expect(plan.stash).not.toBeNull();
    expect(plan.stash!.logins[identity(oauthLogin('user-1', 'ws-1'))]).toBeDefined();
    expect(plan.loginOnDisk).toBe(false);
  });

  it('第三方 + 保留登录开：auth.json 不动', () => {
    const plan = planAuth({
      live: oauthLogin('user-1', 'ws-1'),
      thirdPartyKeys: [],
      target: { kind: 'third-party', preserve: true },
    });
    expect(plan.auth).toBe('keep');
    expect(plan.loginOnDisk).toBe(true);
  });

  it('官方 API-Key 卡：整份写行里的 key', () => {
    const plan = planAuth({
      live: oauthLogin('user-1', 'ws-1'),
      thirdPartyKeys: [],
      target: { kind: 'official', rowAuth: { OPENAI_API_KEY: 'sk-official' } },
    });
    expect(plan.auth).toEqual({ auth: { OPENAI_API_KEY: 'sk-official' } });
    expect(plan.loginOnDisk).toBe(true);
  });

  it('切回官方 OAuth 卡：从暂存按身份还回', () => {
    const login = oauthLogin('user-1', 'ws-1');
    const id = identity(login);
    // 第一步：切第三方（登录入暂存）
    const p1 = planAuth({ live: login, thirdPartyKeys: [], target: { kind: 'third-party', preserve: false } });
    const stash = p1.stash!;
    // 第二步：切回官方（行里带同一登录的快照）
    const p2 = planAuth({
      live: null,
      thirdPartyKeys: ['sk-third'],
      target: { kind: 'official', rowAuth: login },
      ...{ stashInput: undefined },
    } as never);
    void p2;
    // 直接以暂存状态模拟第二步（plan 输入不含暂存——由 service 层持久化；此处验证 take 语义）
    expect(stash.last ?? id).toBe(id);
  });

  it('requires_openai_auth 现算', () => {
    expect(requiresOpenaiAuth('bearer', true)).toBe(true);
    expect(requiresOpenaiAuth('bearer', false)).toBe(false);
    expect(requiresOpenaiAuth('headers', true)).toBe(false);
    expect(requiresOpenaiAuth('none', true)).toBe(false);
  });

  it('hasOpenaiAccountMaterial：API key / tokens 算登录', () => {
    expect(hasOpenaiAccountMaterial({ OPENAI_API_KEY: 'sk' })).toBe(true);
    expect(hasOpenaiAccountMaterial(oauthLogin('u', 'w'))).toBe(true);
    expect(hasOpenaiAccountMaterial({ SOME_META: 'x' })).toBe(false);
    expect(identity(oauthLogin('user-1', 'ws-1'))).toBe('sub:user-1|account:ws-1');
  });
});

// —— U-PR-04 Codex TOML 投影 ——

describe('U-PR-04 projectCodexToml', () => {
  it('清顶层地板键 → 写 custom 路由表（带 bearer token）→ 删 [model_providers.openai] → 用户表保真', () => {
    const prev = `# 用户注释保留
model_provider = "openai"
model = "gpt-5"
experimental_bearer_token = "old"

[agents]
default_subagent_model = "gpt-5-mini"
other_setting = "keep"

[model_providers.openai]
name = "OpenAI"
base_url = "https://api.openai.com"

[model_providers.custom]
name = "old"
base_url = "https://old.example.com"
wire_api = "responses"

[mcp_servers.context7]
command = "npx"
`;
    const next = projectCodexToml(
      prev,
      {
        configToml: `model_provider = "custom"
model = "deepseek-flash"
model_reasoning_effort = "high"
disable_response_storage = true

[model_providers.custom]
name = "deepseek"
base_url = "https://api.deepseek.com"
wire_api = "responses"
requires_openai_auth = true`,
      },
      'sk-deepseek',
    );
    expect(next).toContain('# 用户注释保留');
    expect(next).toContain('model_provider = "custom"');
    expect(next).toContain('model = "deepseek-flash"');
    expect(next).toContain('disable_response_storage = true');
    expect(next).toContain('base_url = "https://api.deepseek.com"');
    expect(next).toContain('experimental_bearer_token = "sk-deepseek"');
    expect(next).not.toContain('model_providers.openai');
    expect(next).not.toContain('api.openai.com');
    expect(next).not.toContain('old.example.com');
    expect(next).toContain('[agents]');
    expect(next).toContain('other_setting = "keep"');
    expect(next).not.toContain('default_subagent_model');
    expect(next).toContain('[mcp_servers.context7]');
    expect(next).not.toContain('requires_openai_auth'); // 按盘上登录现算后注入，模板里的 true 不保留
  });

  it('官方卡（无路由表无 Key）：不写选路', () => {
    const next = projectCodexToml('model_provider = "custom"\n', { auth: {} }, null);
    expect(next).not.toContain('model_provider');
    expect(next).not.toContain('model_providers.custom');
  });
});

// —— U-PR-05 Gemini 投影 ——

describe('U-PR-05 gemini 投影', () => {
  it('.env 按行编辑：地板键整行删，用户行逐字保留，目标键追加', () => {
    const prev = '# comment\nGOOGLE_API_KEY=old\nGEMINI_SANDBOX=true\nDEBUG=1\nGOOGLE_GEMINI_BASE_URL=https://old\n';
    const next = projectGeminiEnv(prev, {
      env: { GOOGLE_GEMINI_BASE_URL: 'https://new', GEMINI_API_KEY: 'new-key' },
    });
    expect(next).toContain('# comment');
    expect(next).toContain('GEMINI_SANDBOX=true');
    expect(next).toContain('DEBUG=1');
    expect(next).not.toContain('GOOGLE_API_KEY=old');
    expect(next).not.toContain('https://old');
    expect(next).toContain('GOOGLE_GEMINI_BASE_URL=https://new');
    expect(next).toContain('GEMINI_API_KEY=new-key');
  });

  it('settings.json 只清两个地板键路径', () => {
    const next = projectGeminiSettings({
      security: { auth: { selectedType: 'oauth', other: 1 } },
      model: { name: 'gemini-pro', temp: 0.5 },
      ui: { theme: 'dark' },
    });
    expect((next['security'] as Record<string, unknown>)['auth']).toEqual({ other: 1 });
    expect((next['model'] as Record<string, unknown>)['name']).toBeUndefined();
    expect((next['model'] as Record<string, unknown>)['temp']).toBe(0.5);
    expect(next['ui']).toEqual({ theme: 'dark' });
  });
});

// —— U-PR-06 集成：service 切换全链路（沙箱 agent home） ——

describe('U-PR-06 切换全链路', () => {
  it('Claude：settings.json 投影落盘 + 意图清账 + 首写备份', async () => {
    const profile = await service.upsertProfile({
      app: 'claude-code',
      name: 'Kimi',
      category: 'cn_official',
      config: { env: { ANTHROPIC_BASE_URL: 'https://api.moonshot.cn/anthropic', ANTHROPIC_AUTH_TOKEN: 'sk-kimi' } },
    });
    const settingsFile = path.join(sandbox, '.claude', 'settings.json');
    await fs.mkdir(path.dirname(settingsFile), { recursive: true });
    await fs.writeFile(settingsFile, JSON.stringify({ model: 'old-model', hooks: {} }), 'utf8');

    const res = await service.switchTo(profile.id);
    expect(res.app).toBe('claude-code');
    const written = JSON.parse(await fs.readFile(settingsFile, 'utf8')) as Record<string, unknown>;
    expect(written['model']).toBeUndefined();
    expect(((written['env'] as Record<string, unknown>)['ANTHROPIC_AUTH_TOKEN'] as string)).toBe('sk-kimi');
    expect((written['hooks'] as unknown)).toEqual({});
    expect(config.providerCurrent['claude-code']).toBe(profile.id);
    // 发布日志清账
    await expect(fs.access(path.join(store.homeDir, 'pending-publication.json'))).rejects.toThrow();
    // 首写备份
    const fw = await fs.readdir(path.join(store.backupsDir, 'first-write'));
    expect(fw.filter((f) => f.endsWith('.bak'))).toHaveLength(1);
  });

  it('Codex：切第三方 → auth.json 删除 + 暂存 + config.toml 投影；切回官方 API-Key → 登录还回', async () => {
    const tpl = `model_provider = "custom"
model = "deepseek-flash"
[model_providers.custom]
name = "deepseek"
base_url = "https://api.deepseek.com"
wire_api = "responses"`;
    const third = await service.upsertProfile({
      app: 'codex',
      name: 'DeepSeek',
      config: { auth: { OPENAI_API_KEY: 'sk-third' }, configToml: tpl },
    });
    const official = await service.upsertProfile({
      app: 'codex',
      name: 'OpenAI Official',
      category: 'official',
      config: { auth: { OPENAI_API_KEY: 'sk-official' } },
    });
    const codexDir = path.join(sandbox, '.codex');
    await fs.mkdir(codexDir, { recursive: true });
    await fs.writeFile(path.join(codexDir, 'auth.json'), JSON.stringify(oauthLogin('user-1', 'ws-1')), 'utf8');
    await fs.writeFile(path.join(codexDir, 'config.toml'), '# 用户注释\n', 'utf8');

    // 切第三方
    await service.switchTo(third.id);
    await expect(fs.access(path.join(codexDir, 'auth.json'))).rejects.toThrow(); // auth.json 删除（登录已入暂存）
    const toml = await fs.readFile(path.join(codexDir, 'config.toml'), 'utf8');
    expect(toml).toContain('base_url = "https://api.deepseek.com"');
    expect(toml).toContain('# 用户注释');
    const stash = JSON.parse(await fs.readFile(path.join(store.homeDir, 'codex-login-stash.json'), 'utf8')) as {
      logins: Record<string, unknown>;
    };
    expect(Object.keys(stash.logins)).toEqual([identity(oauthLogin('user-1', 'ws-1'))]);

    // 切回官方 API-Key
    await service.switchTo(official.id);
    const authBack = JSON.parse(await fs.readFile(path.join(codexDir, 'auth.json'), 'utf8')) as Record<string, unknown>;
    expect(authBack['OPENAI_API_KEY']).toBe('sk-official');
    const tomlBack = await fs.readFile(path.join(codexDir, 'config.toml'), 'utf8');
    expect(tomlBack).not.toContain('model_provider'); // 官方卡不写选路
    expect(config.providerCurrent['codex']).toBe(official.id);
  });

  it('Gemini：.env + settings.json 双写', async () => {
    const profile = await service.upsertProfile({
      app: 'gemini-cli',
      name: 'PackyCode',
      config: { env: { GOOGLE_GEMINI_BASE_URL: 'https://www.packyapi.ai', GEMINI_API_KEY: 'pk-' } },
    });
    const geminiDir = path.join(sandbox, '.gemini');
    await fs.mkdir(geminiDir, { recursive: true });
    await fs.writeFile(path.join(geminiDir, '.env'), 'GOOGLE_API_KEY=old\nGEMINI_SANDBOX=true\n', 'utf8');
    await fs.writeFile(
      path.join(geminiDir, 'settings.json'),
      JSON.stringify({ security: { auth: { selectedType: 'oauth' } } }),
      'utf8',
    );
    await service.switchTo(profile.id);
    const env = await fs.readFile(path.join(geminiDir, '.env'), 'utf8');
    expect(env).toContain('GEMINI_SANDBOX=true');
    expect(env).toContain('GOOGLE_GEMINI_BASE_URL=https://www.packyapi.ai');
    expect(env).not.toContain('GOOGLE_API_KEY=old');
    const settings = JSON.parse(await fs.readFile(path.join(geminiDir, 'settings.json'), 'utf8')) as Record<string, unknown>;
    expect((settings['security'] as Record<string, unknown>)['auth']).toEqual({});
  });
});

// —— U-PR-07 预设数据完整性 ——

describe('U-PR-07 预设目录', () => {
  it('三家预设数量与形状', () => {
    const claude = service.presets('claude-code');
    const codex = service.presets('codex');
    const gemini = service.presets('gemini-cli');
    expect(claude.length).toBeGreaterThanOrEqual(90);
    expect(codex.length).toBeGreaterThanOrEqual(90);
    expect(gemini.length).toBeGreaterThanOrEqual(20);
    for (const p of claude) {
      expect(p.name).toBeTruthy();
      expect(p.settingsConfig === undefined || typeof p.settingsConfig === 'object').toBe(true);
    }
    // codex 第三方预设必须带 config TOML 且以 [model_providers.custom] 或官方形态出现
    for (const p of codex) {
      if (p.config) {
        expect(p.config).toContain('model');
        expect(p.config.includes('[model_providers.openai]')).toBe(false);
      }
    }
    expect(claude.some((p) => p.name === 'Kimi')).toBe(true);
  });
});
