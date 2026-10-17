import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { makeAgents } from '../src/agents.js';
import { Store } from '../src/store.js';
import { SyncEngine } from '../src/sync.js';
import {
  applyImport,
  detectAgents,
  importPreview,
  setBinding,
  upsertServer,
} from '../src/registry.js';
import { GATEWAY_KEY } from '../src/types.js';
import type { ManagerConfig } from '../src/types.js';

let sandbox: string;
let agents: ReturnType<typeof makeAgents>;
let store: Store;
let engine: SyncEngine;
let config: ManagerConfig;

beforeEach(async () => {
  sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-sync-'));
  agents = makeAgents(sandbox);
  store = new Store(sandbox);
  engine = new SyncEngine({
    backupsDir: store.backupsDir,
    agents,
    stdioBridge: { command: 'node', baseArgs: ['C:/tools/beaver/bridge-main.js'] },
  });
  config = await store.load();
});

afterEach(async () => {
  await fs.rm(sandbox, { recursive: true, force: true });
});

function addStdioServer(id: string, name: string): void {
  upsertServer(config, {
    id,
    name,
    transport: 'stdio',
    command: 'npx',
    args: ['-y', `@modelcontextprotocol/server-${id}`],
    env: { TOKEN: `secret-${id}` },
  });
}

async function writeAgentFile(relSegments: string[], content: string): Promise<void> {
  const file = path.join(sandbox, ...relSegments);
  await fs.mkdir(path.dirname(file), { recursive: true });
  await fs.writeFile(file, content, 'utf8');
}

describe('detect + import (U-IM / C1 C5)', () => {
  it('探测：只认存在配置文件的 agent', async () => {
    await fs.mkdir(path.join(sandbox, '.cursor'), { recursive: true });
    await fs.writeFile(
      path.join(sandbox, '.cursor', 'mcp.json'),
      JSON.stringify({ mcpServers: { ctx: { command: 'node', args: ['ctx.js'] } } }),
      'utf8',
    );
    const detections = await detectAgents(agents);
    const byType = new Map(detections.map((d) => [d.agentType, d]));
    expect(byType.get('cursor')!.detected).toBe(true);
    expect(byType.get('cursor')!.configPaths).toEqual([path.join(sandbox, '.cursor', 'mcp.json')]);
    expect(byType.get('claude-code')!.detected).toBe(false);
    expect(byType.get('claude-code')!.configPaths).toEqual([path.join(sandbox, '.claude.json')]);
  });

  it('导入：跨 agent 指纹去重，applyImport 跳过注册表已有项', async () => {
    await fs.mkdir(path.join(sandbox, '.cursor'), { recursive: true });
    await fs.mkdir(path.join(sandbox, '.claude'), { recursive: true });
    await fs.writeFile(
      path.join(sandbox, '.cursor', 'mcp.json'),
      JSON.stringify({ mcpServers: { fs: { command: 'npx', args: ['-y', 'srv-fs'] } } }),
      'utf8',
    );
    await fs.writeFile(
      path.join(sandbox, '.claude.json'),
      JSON.stringify({ mcpServers: { fs2: { command: 'npx', args: ['-y', 'srv-fs'] }, gh: { command: 'node', args: ['gh.js'] } } }),
      'utf8',
    );
    const groups = await importPreview(agents);
    expect(groups).toHaveLength(2); // srv-fs 两个 agent 同指纹 → 1 组；gh 1 组
    const fsGroup = groups.find((g) => g.fingerprint.includes('srv-fs'))!;
    expect(fsGroup.candidates).toHaveLength(2);

    const added = applyImport(config, groups);
    expect(added).toHaveLength(2);
    // 分组的 id 取首个候选（AGENT_LIST 顺序 claude-code 在前 → fs2）
    expect(config.servers.map((s) => s.id).sort()).toEqual(['fs2', 'gh']);

    // 再导一次：指纹重复 → 0 新增
    const groups2 = await importPreview(agents);
    expect(applyImport(config, groups2)).toHaveLength(0);
  });

  it('Issue-1：导入预览排除网关公共条目 beaver-gateway（含更名前旧键）', async () => {
    await fs.mkdir(path.join(sandbox, '.cursor'), { recursive: true });
    await fs.writeFile(
      path.join(sandbox, '.cursor', 'mcp.json'),
      JSON.stringify({
        mcpServers: {
          'beaver-gateway': { url: 'http://127.0.0.1:6280/mcp', headers: { Authorization: 'Bearer x' } },
          'agent-control-gateway': { url: 'http://127.0.0.1:6280/mcp', headers: { Authorization: 'Bearer x' } },
          real: { command: 'node', args: ['real.js'] },
        },
      }),
      'utf8',
    );
    const groups = await importPreview(agents);
    expect(groups).toHaveLength(1);
    expect(groups[0]!.suggested.name).toBe('real');
  });
});

describe('sync engine (I-SY)', () => {
  it('I-SY-01 绑定矩阵 → 三个 agent 配置落盘，无关内容保真', async () => {
    // 预置用户已有内容
    await writeAgentFile(['.claude.json'], JSON.stringify({ theme: 'dark', mcpServers: { user1: { command: 'x' } } }));
    await writeAgentFile(['.codex', 'config.toml'], '# my config\nmodel = "x"\n');
    await writeAgentFile(['.cursor', 'mcp.json'], JSON.stringify({ mcpServers: {} }));

    addStdioServer('filesystem', 'Filesystem');
    setBinding(config, 'filesystem', 'claude-code');
    setBinding(config, 'filesystem', 'codex');
    setBinding(config, 'filesystem', 'cursor');

    const report = await engine.sync(config);
    await store.save(config);

    const claude = JSON.parse(await fs.readFile(path.join(sandbox, '.claude.json'), 'utf8'));
    expect(claude.theme).toBe('dark');
    expect(claude.mcpServers['user1']).toEqual({ command: 'x' });
    expect(claude.mcpServers['filesystem'].command).toBe('npx');
    expect(claude.mcpServers['filesystem'].env.TOKEN).toBe('secret-filesystem');

    const codexText = await fs.readFile(path.join(sandbox, '.codex', 'config.toml'), 'utf8');
    expect(codexText).toContain('# my config');
    expect(codexText).toContain('[mcp_servers.filesystem]');

    const cursor = JSON.parse(await fs.readFile(path.join(sandbox, '.cursor', 'mcp.json'), 'utf8'));
    expect(cursor.mcpServers['filesystem'].command).toBe('npx');

    // 报告：三个绑定的 agent 各一个 upsert
    const boundAgents = ['claude-code', 'codex', 'cursor'];
    for (const agent of report.perAgent.filter((r) => boundAgents.includes(r.agentType))) {
      expect(agent.changes).toContainEqual({
        key: 'filesystem',
        action: 'upsert',
        serverId: 'filesystem',
      });
    }

    // 幂等：再同步全部 no-change
    const report2 = await engine.sync(config);
    for (const agent of report2.perAgent) {
      expect(agent.changes.every((c) => c.action === 'none')).toBe(true);
    }
  });

  it('I-SY-02 解绑 → 收敛移除片段', async () => {
    addStdioServer('filesystem', 'Filesystem');
    setBinding(config, 'filesystem', 'cursor');
    await engine.sync(config);
    await store.save(config);
    expect(
      JSON.parse(await fs.readFile(path.join(sandbox, '.cursor', 'mcp.json'), 'utf8')).mcpServers[
        'filesystem'
      ],
    ).toBeDefined();

    config.bindings = config.bindings.filter((b) => !(b.serverId === 'filesystem' && b.agentType === 'cursor'));
    const report = await engine.sync(config);
    await store.save(config);
    const cursorReport = report.perAgent.find((r) => r.agentType === 'cursor')!;
    expect(cursorReport.changes).toContainEqual({ key: 'filesystem', action: 'remove', serverId: undefined });
    const doc = JSON.parse(await fs.readFile(path.join(sandbox, '.cursor', 'mcp.json'), 'utf8'));
    expect(doc.mcpServers['filesystem']).toBeUndefined();
  });

  it('I-SY-03 网关模式：http agent 写 GATEWAY_KEY；Codex 经 G7 反向桥接入网关', async () => {
    await writeAgentFile(['.codex', 'config.toml'], '# my config\n');
    addStdioServer('notion', 'Notion');
    config.servers[0]!.gatewayMode = true;
    setBinding(config, 'notion', 'claude-code');
    setBinding(config, 'notion', 'codex');

    const report = await engine.sync(config);
    await store.save(config);

    const claude = JSON.parse(await fs.readFile(path.join(sandbox, '.claude.json'), 'utf8'));
    expect(claude.mcpServers['notion']).toBeUndefined();
    // D7 默认关闭令牌 → 网关条目不带凭证
    expect(claude.mcpServers[GATEWAY_KEY]).toEqual({
      type: 'http',
      url: `http://127.0.0.1:${config.settings.port}/agents/claude-code/mcp`,
    });

    const codexText = await fs.readFile(path.join(sandbox, '.codex', 'config.toml'), 'utf8');
    expect(codexText).toContain('[mcp_servers.beaver-gateway]');
    expect(codexText).toContain('bridge-main.js');
    expect(codexText).toContain(`http://127.0.0.1:${config.settings.port}/agents/codex/mcp`);
    expect(codexText).not.toContain('--token=');
    const codexReport = report.perAgent.find((r) => r.agentType === 'codex')!;
    expect(codexReport.unsupported).toHaveLength(0);

    // 开启令牌 → 重写后带凭证（claude 带 Authorization；codex 桥带 --token）
    config.settings.authRequired = true;
    await engine.sync(config);
    const claude2 = JSON.parse(await fs.readFile(path.join(sandbox, '.claude.json'), 'utf8'));
    expect(claude2.mcpServers[GATEWAY_KEY].headers).toEqual({
      Authorization: `Bearer ${config.settings.token}`,
    });
    expect(await fs.readFile(path.join(sandbox, '.codex', 'config.toml'), 'utf8')).toContain(
      `--token=${config.settings.token}`,
    );
  });

  it('I-SY-04 直连 http server 在 Codex 上经反向桥分发', async () => {
    await writeAgentFile(['.codex', 'config.toml'], '# my config\n');
    upsertServer(config, {
      id: 'web-api', name: 'Web API', transport: 'http', url: 'https://api.example.com/mcp',
      headers: { 'X-API-Key': 'k-1' },
    });
    setBinding(config, 'web-api', 'codex');
    const report = await engine.sync(config);
    const codexReport = report.perAgent.find((r) => r.agentType === 'codex')!;
    expect(codexReport.unsupported).toHaveLength(0);
    const codexText = await fs.readFile(path.join(sandbox, '.codex', 'config.toml'), 'utf8');
    expect(codexText).toContain('[mcp_servers.web-api]');
    expect(codexText).toContain('https://api.example.com/mcp');
    expect(codexText).toContain('--header');
    expect(codexText).toContain('X-API-Key=k-1');
  });

  it('I-SY-05 手改片段 → 冲突报告；override 后覆盖', async () => {
    await writeAgentFile(['.cursor', 'mcp.json'], JSON.stringify({ mcpServers: {} }));
    addStdioServer('filesystem', 'Filesystem');
    setBinding(config, 'filesystem', 'cursor');
    await engine.sync(config);
    await store.save(config);

    // 用户手改
    const doc = JSON.parse(await fs.readFile(path.join(sandbox, '.cursor', 'mcp.json'), 'utf8'));
    doc.mcpServers['filesystem'].args = ['-y', 'hacked'];
    await fs.writeFile(path.join(sandbox, '.cursor', 'mcp.json'), JSON.stringify(doc), 'utf8');

    const conflict = await engine.sync(config);
    const cr = conflict.perAgent.find((r) => r.agentType === 'cursor')!;
    expect(cr.conflicts).toHaveLength(1);
    expect(cr.conflicts[0]!.resolutionKey).toBe('cursor:filesystem');

    // override
    const overrideReport = await engine.sync(config, {
      resolutions: { 'cursor:filesystem': 'override' },
    });
    const or = overrideReport.perAgent.find((r) => r.agentType === 'cursor')!;
    expect(or.conflicts[0]!.resolution).toBe('override');
    const after = JSON.parse(await fs.readFile(path.join(sandbox, '.cursor', 'mcp.json'), 'utf8'));
    expect(after.mcpServers['filesystem'].args).toEqual(['-y', '@modelcontextprotocol/server-filesystem']);
  });

  it('I-SY-06 备份：真实变更在 backups 目录留下按 agent 分组的副本', async () => {
    await writeAgentFile(['.claude.json'], JSON.stringify({ mcpServers: {} }));
    addStdioServer('filesystem', 'Filesystem');
    setBinding(config, 'filesystem', 'claude-code');
    await engine.sync(config);
    const backupDir = path.join(store.backupsDir, 'claude-code');
    const files = await fs.readdir(backupDir);
    expect(files.length).toBe(1);
    expect(files[0]).toMatch(/claude\.json$/);
  });
});

describe('ZCode 嵌套容器 + WorkBuddy 适配（M3.7）', () => {
  it('ZCode：mcp.servers 嵌套读写，管辖子树之外的键原样保留', async () => {
    await writeAgentFile(
      ['.zcode', 'cli', 'config.json'],
      JSON.stringify({
        mcp: {
          servers: { 'user-own': { type: 'http', url: 'https://example.com/mcp' } },
          otherSetting: { keep: true },
        },
        plugins: { 'some-plugin': true },
        hooks: { enabled: false },
      }),
    );
    // 导入预览能读到嵌套容器里的条目
    const groups = await importPreview(agents);
    const fromZcode = groups.flatMap((g) => g.candidates).filter((c) => c.agentType === 'zcode');
    expect(fromZcode).toHaveLength(1);

    // 分发一个 stdio server 到 zcode
    addStdioServer('filesystem', 'Filesystem');
    setBinding(config, 'filesystem', 'zcode');
    await engine.sync(config);

    const after = JSON.parse(await fs.readFile(path.join(sandbox, '.zcode', 'cli', 'config.json'), 'utf8'));
    // 只写官方字段（ZCode schema 严格，未知键会被客户端丢弃）
    expect(after.mcp.servers['filesystem']).toEqual({
      command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-filesystem'],
      env: { TOKEN: 'secret-filesystem' },
    });
    // 用户原有条目 + 嵌套兄弟键 + 顶层其他键全部保留
    expect(after.mcp.servers['user-own']).toEqual({ type: 'http', url: 'https://example.com/mcp' });
    expect(after.mcp.otherSetting).toEqual({ keep: true });
    expect(after.plugins).toEqual({ 'some-plugin': true });
    expect(after.hooks).toEqual({ enabled: false });
  });

  it('ZCode：首次分发（文件无 mcp 键）创建嵌套容器，写后校验通过', async () => {
    // 回归：中间层 mcp 由写入新建时，校验须剪掉残留空壳而非误判回滚
    await writeAgentFile(
      ['.zcode', 'cli', 'config.json'],
      JSON.stringify({ plugins: { 'some-plugin': true }, hooks: { enabled: false } }),
    );
    addStdioServer('filesystem', 'Filesystem');
    setBinding(config, 'filesystem', 'zcode');
    await engine.sync(config);

    const after = JSON.parse(await fs.readFile(path.join(sandbox, '.zcode', 'cli', 'config.json'), 'utf8'));
    expect(after.mcp.servers['filesystem']).toEqual({
      command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-filesystem'],
      env: { TOKEN: 'secret-filesystem' },
    });
    expect(after.plugins).toEqual({ 'some-plugin': true });
    expect(after.hooks).toEqual({ enabled: false });
  });

  it('ZCode：mcp 为空对象可正常创建 servers；mcp 非法形状拒绝写入且文件不动', async () => {
    await writeAgentFile(['.zcode', 'cli', 'config.json'], JSON.stringify({ mcp: {} }));
    addStdioServer('filesystem', 'Filesystem');
    setBinding(config, 'filesystem', 'zcode');
    await engine.sync(config);
    const after = JSON.parse(await fs.readFile(path.join(sandbox, '.zcode', 'cli', 'config.json'), 'utf8'));
    expect(after.mcp.servers['filesystem'].command).toBe('npx');

    const broken = JSON.stringify({ mcp: 'not-an-object' });
    await writeAgentFile(['.zcode', 'cli', 'config.json'], broken);
    await expect(engine.sync(config)).rejects.toMatchObject({ code: 'MALFORMED_CONFIG' });
    expect(await fs.readFile(path.join(sandbox, '.zcode', 'cli', 'config.json'), 'utf8')).toBe(broken);
  });

  it('WorkBuddy：streamableHttp 归一化为 http；未管辖条目的 timeout/disabled 保留', async () => {
    await writeAgentFile(
      ['.workbuddy', 'mcp.json'],
      JSON.stringify({
        mcpServers: {
          remote: { type: 'streamableHttp', url: 'https://light.example/mcp', timeout: 30000, disabled: false },
        },
      }),
    );
    const detections = await detectAgents(agents);
    const wb = detections.find((d) => d.agentType === 'workbuddy')!;
    expect(wb.detected).toBe(true);

    addStdioServer('filesystem', 'Filesystem');
    setBinding(config, 'filesystem', 'workbuddy');
    await engine.sync(config);

    const after = JSON.parse(await fs.readFile(path.join(sandbox, '.workbuddy', 'mcp.json'), 'utf8'));
    // 新写入的条目用 WorkBuddy 的 type 命名
    expect(after.mcpServers['filesystem']).toEqual({
      command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-filesystem'],
      env: { TOKEN: 'secret-filesystem' },
    });
    // 未管辖的既有条目原样保留（含 WorkBuddy 特有字段）
    expect(after.mcpServers['remote']).toEqual({
      type: 'streamableHttp',
      url: 'https://light.example/mcp',
      timeout: 30000,
      disabled: false,
    });
  });
});
