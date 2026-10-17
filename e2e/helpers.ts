import { spawn, type ChildProcess } from 'node:child_process';
import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);

export interface TestDaemon {
  base: string;
  token: string;
  homeDir: string;
  stop: () => Promise<void>;
}

/**
 * E2E 沙箱纪律：数据目录与 agent 配置根都指向临时目录，
 * 绝不触碰真实 agent 配置（与单元/集成测试同一约束）。
 */
export async function startTestDaemon(port = 6290): Promise<TestDaemon> {
  const homeDir = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-e2e-'));
  const serverDist = path.dirname(require.resolve('@beaver/server'));
  const child: ChildProcess = spawn(
    process.execPath,
    [serverDist, '--home', homeDir, '--agent-home', homeDir, '--port', String(port)],
    { stdio: 'ignore', windowsHide: true },
  );

  // 读取 token（daemon 启动时会生成默认配置）
  const storePath = path.join(homeDir, 'config.json');
  let token = '';
  for (let i = 0; i < 100; i++) {
    try {
      const config = JSON.parse(await fs.readFile(storePath, 'utf8')) as { settings: { token: string } };
      token = config.settings.token;
      break;
    } catch {
      await new Promise((r) => setTimeout(r, 100));
    }
  }
  const base = `http://127.0.0.1:${port}`;
  // 等待 API 就绪
  for (let i = 0; i < 100; i++) {
    try {
      const res = await fetch(`${base}/api/status`, { headers: { authorization: `Bearer ${token}` } });
      if (res.ok) break;
    } catch {
      /* 未就绪 */
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  return {
    base,
    token,
    homeDir,
    stop: async () => {
      child.kill();
      await new Promise((r) => setTimeout(r, 500));
      await fs.rm(homeDir, { recursive: true, force: true }).catch(() => {});
    },
  };
}
