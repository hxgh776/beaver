import { Command } from 'commander';
import { autostartInstall, autostartIsInstalled, autostartUninstall } from './autostart.js';
import { Store } from '@beaver/core';
import { runBridgeStdio } from '@beaver/server/gateway/bridge';
import {
  isPidAlive,
  killTree,
  readPid,
  removePidFile,
  resolveDaemonEntry,
  spawnDetached,
} from '@beaver/server/process';

const program = new Command();
program.name('dam').description('Beaver（河狸）——本地 Agent 能力总控台').version('0.1.0');

let homeOverride: string | undefined;
program
  .option('--home <dir>', '数据目录（缺省 ~/.beaver）')
  .hook('preAction', (cmd) => {
    homeOverride = cmd.opts<{ home?: string }>().home;
  });

function store(): Store {
  return new Store(homeOverride);
}

async function api<T>(
  method: string,
  pathname: string,
  body?: unknown,
): Promise<{ status: number; data: T }> {
  const s = store();
  const config = await s.load();
  const base = `http://127.0.0.1:${config.settings.port}`;
  let res: Response;
  try {
    res = await fetch(`${base}${pathname}`, {
      method,
      headers: {
        authorization: `Bearer ${config.settings.token}`,
        ...(body !== undefined ? { 'content-type': 'application/json' } : {}),
      },
      ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    });
  } catch {
    throw new Error(`daemon 未运行（${base}），请先执行 dam start`);
  }
  const data = (await res.json()) as T;
  return { status: res.status, data };
}

function fail(message: string): never {
  console.error(`✗ ${message}`);
  process.exitCode = 1;
  throw new Error('exit');
}

program
  .command('start')
  .description('启动 daemon（分离式后台进程）')
  .action(async () => {
    const s = store();
    await s.load(); // 确保配置与 token 存在
    const pid = await readPid(s.pidPath);
    if (pid && isPidAlive(pid)) {
      const config = await s.load();
      console.log(`daemon 已在运行 (pid ${pid})：http://127.0.0.1:${config.settings.port}`);
      return;
    }
    const entry = resolveDaemonEntry();
    const childPid = spawnDetached(entry, homeOverride);
    // 等待 API 就绪
    const config = await s.load();
    const base = `http://127.0.0.1:${config.settings.port}`;
    for (let i = 0; i < 50; i++) {
      try {
        const res = await fetch(`${base}/api/status`, {
          headers: { authorization: `Bearer ${config.settings.token}` },
        });
        if (res.ok) {
          console.log(`daemon 已启动 (pid ${childPid})`);
          console.log(`  API/UI: ${base}`);
          console.log(`  MCP   : ${base}/mcp`);
          return;
        }
      } catch {
        // 未就绪
      }
      await new Promise((r) => setTimeout(r, 200));
    }
    fail('daemon 启动超时，请查看日志: ~/.beaver/logs/daemon.log');
  });

program
  .command('stop')
  .description('停止 daemon')
  .action(async () => {
    const s = store();
    const pid = await readPid(s.pidPath);
    if (!pid || !isPidAlive(pid)) {
      await removePidFile(s.pidPath);
      console.log('daemon 未在运行');
      return;
    }
    await killTree(pid);
    await removePidFile(s.pidPath);
    console.log(`daemon 已停止 (pid ${pid})`);
  });

program
  .command('status')
  .description('查看 daemon 状态')
  .action(async () => {
    const { status, data } = await api<Record<string, unknown>>('GET', '/api/status');
    if (status !== 200) fail(`status ${status}`);
    console.log(JSON.stringify(data, null, 2));
  });

const servers = program.command('servers').description('MCP server 注册表');
servers
  .command('list')
  .description('列出全部 server')
  .action(async () => {
    const { data } = await api<{ servers: Array<Record<string, unknown>> }>('GET', '/api/servers');
    if (data.servers.length === 0) {
      console.log('（空）先用 dam import 或 servers add 添加');
      return;
    }
    for (const s of data.servers) {
      console.log(
        `${String(s['id']).padEnd(24)} ${String(s['transport']).padEnd(6)} gateway=${String(s['gatewayMode'])} enabled=${String(s['enabled'])}  ${String(s['name'])}`,
      );
    }
  });

servers
  .command('add')
  .description('添加 server（stdio: --command/--args；http: --url）')
  .requiredOption('--name <name>', '名称')
  .requiredOption('--transport <type>', 'stdio | http')
  .option('--command <cmd>', 'stdio 命令')
  .option('--args <args...>', 'stdio 参数')
  .option('--env <key=value>', '环境变量（可重复）', (v: string, prev: string[] = []) => [...prev, v])
  .option('--url <url>', 'http 地址')
  .option('--gateway', '使用网关模式分发', false)
  .action(async (opts) => {
    const env: Record<string, string> = {};
    for (const kv of (opts.env ?? []) as string[]) {
      const idx = kv.indexOf('=');
      if (idx > 0) env[kv.slice(0, idx)] = kv.slice(idx + 1);
    }
    const { status, data } = await api<{ server: { id: string } }>('POST', '/api/servers', {
      name: opts.name,
      transport: opts.transport,
      command: opts.command,
      args: opts.args,
      env: Object.keys(env).length > 0 ? env : undefined,
      url: opts.url,
      gatewayMode: opts.gateway,
    });
    if (status !== 201) fail(`添加失败: ${JSON.stringify(data)}`);
    console.log(`已添加: ${data.server.id}`);
  });

program
  .command('agents')
  .description('查看探测到的 agent')
  .action(async () => {
    const { data } = await api<{
      agents: Array<{ agentType: string; displayName: string; detected: boolean; boundServerIds: string[] }>;
    }>('GET', '/api/agents?refresh=1');
    for (const a of data.agents) {
      console.log(
        `${a.detected ? '●' : '○'} ${a.displayName.padEnd(14)} ${a.agentType.padEnd(14)} 绑定: ${a.boundServerIds.join(', ') || '无'}`,
      );
    }
  });

program
  .command('import')
  .description('从现有 agent 配置导入 server 定义')
  .option('--all', '导入全部候选', false)
  .action(async (opts) => {
    const { data } = await api<{
      groups: Array<{ fingerprint: string; alreadyImported: boolean; suggested: { name: string }; candidates: unknown[] }>;
    }>('GET', '/api/import/preview');
    const pending = data.groups.filter((g) => !g.alreadyImported);
    if (pending.length === 0) {
      console.log('没有可导入的新 server');
      return;
    }
    for (const g of pending) {
      console.log(`${g.suggested.name.padEnd(24)} ← ${g.candidates.length} 处  ${g.fingerprint}`);
    }
    if (!opts.all) {
      // 未指定 --all 时仅预览
      console.log('\n（预览模式，执行 dam import --all 以导入）');
      return;
    }
    const { data: added } = await api<{ added: Array<{ id: string }> }>('POST', '/api/import', {
      fingerprints: pending.map((g) => g.fingerprint),
    });
    console.log(`已导入 ${added.added.length} 个 server`);
  });

program
  .command('sync')
  .description('把注册表分发到各 agent（--dry-run 仅预览）')
  .option('--dry-run', '仅预览变更', false)
  .action(async (opts) => {
    const { data } = await api<{
      report: {
        perAgent: Array<{
          agentType: string;
          changes: { key: string; action: string }[];
          conflicts: { key: string }[];
          unsupported: { serverId: string; reason: string }[];
        }>;
      };
    }>('POST', '/api/sync', { dryRun: opts.dryRun });
    for (const agent of data.report.perAgent) {
      const changes = agent.changes.filter((c) => c.action !== 'none');
      if (changes.length === 0 && agent.conflicts.length === 0 && agent.unsupported.length === 0) continue;
      console.log(`[${agent.agentType}]`);
      for (const c of changes) console.log(`  ${c.action.padEnd(7)} ${c.key}`);
      for (const c of agent.conflicts) console.log(`  ⚠ 冲突 ${c['key']}（用 Web UI 处置或覆盖）`);
      for (const u of agent.unsupported) console.log(`  ! 跳过 ${u.serverId}: ${u.reason}`);
    }
    console.log(opts.dryRun ? '\n（dry-run，未落盘）' : '\n同步完成');
  });

program
  .command('bridge')
  .description('G7 stdio 反向桥：把 streamable HTTP 上游暴露为本地 stdio MCP server')
  .argument('<url>', '上游 streamable HTTP 地址')
  .option('--token <token>', '以 Bearer token 鉴权')
  .option('--header <k=v>', '附加请求头（可重复）', (v: string, prev: string[] = []) => [...prev, v])
  .option('--name <name>', '桥 server 名称')
  .action(async (url: string, opts: { token?: string; header?: string[]; name?: string }) => {
    const headers: Record<string, string> = {};
    for (const kv of opts.header ?? []) {
      const idx = kv.indexOf('=');
      if (idx > 0) headers[kv.slice(0, idx)] = kv.slice(idx + 1);
    }
    if (opts.token) headers['Authorization'] = `Bearer ${opts.token}`;
    console.error(`[dam bridge] → ${url}`);
    await runBridgeStdio({
      url,
      ...(Object.keys(headers).length > 0 ? { headers } : {}),
      ...(opts.name ? { name: opts.name } : {}),
    });
  });

const gateway = program.command('gateway').description('MCP 网关');gateway
  .command('status')
  .action(async () => {
    const { data } = await api<{ running: boolean; port?: number }>('GET', '/api/gateway');
    console.log(data.running ? `网关运行中: http://127.0.0.1:${data.port}/mcp` : '网关未运行');
  });
for (const action of ['start', 'stop'] as const) {
  gateway
    .command(action)
    .action(async () => {
      const { status, data } = await api<{ running: boolean; port?: number }>('POST', `/api/gateway/${action}`);
      if (status !== 200) fail(String((data as { error?: string }).error));
      console.log(data.running ? `网关已启动: http://127.0.0.1:${data.port}/mcp` : '网关已停止');
    });
}

const autostart = program.command('autostart').description('开机自启 daemon（登录时静默拉起，start 幂等）');
autostart
  .command('on')
  .description('安装开机自启（Windows: HKCU Run 键；macOS: LaunchAgent；Linux: systemd user unit）')
  .action(async () => {
    const cmdLine = autostartInstall(homeOverride);
    console.log('已安装开机自启，下次登录自动拉起 daemon');
    console.log(`  ${cmdLine}`);
    console.log('  撤销: dam autostart off');
  });
autostart
  .command('off')
  .description('移除开机自启')
  .action(async () => {
    autostartUninstall();
    console.log('已移除开机自启');
  });
autostart
  .command('status')
  .description('查看自启安装状态')
  .action(async () => {
    console.log(autostartIsInstalled() ? '已安装' : '未安装（dam autostart on 安装）');
  });

program.parseAsync(process.argv).catch((err) => {
  if (err.message !== 'exit') console.error(err);
  process.exitCode = 1;
});
