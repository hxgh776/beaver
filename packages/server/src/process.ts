import { spawn } from 'node:child_process';
import { createRequire } from 'node:module';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { existsSync } from 'node:fs';
import { readTextIfExists } from '@beaver/core';

/** 解析 daemon 入口：
 *  - npm 安装态：与 CLI 同目录的 bundle（dist/daemon.js，esbuild 双入口产物）
 *  - workspace 开发态：@beaver/server 包的 dist/index.js
 */
export function resolveDaemonEntry(): string {
  const bundled = path.resolve(import.meta.dirname, 'daemon.js');
  if (existsSync(bundled)) return bundled;
  const require = createRequire(import.meta.url);
  return require.resolve('@beaver/server');
}

export async function readPid(pidPath: string): Promise<number | null> {
  const text = await readTextIfExists(pidPath);
  if (text === null) return null;
  const pid = Number(text.trim());
  return Number.isInteger(pid) && pid > 0 ? pid : null;
}

export function isPidAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    return (err as NodeJS.ErrnoException).code === 'EPERM';
  }
}

/** 分离式启动 daemon：脱离父进程生命周期，Windows/POSIX 通用 */
export function spawnDetached(entryFile: string, homeDir?: string): number {
  const env = homeDir ? { ...process.env, AGENT_CONTROL_HOME: homeDir } : { ...process.env };
  const child = spawn(process.execPath, [entryFile], {
    detached: true,
    stdio: 'ignore',
    env,
    windowsHide: true,
  });
  child.unref();
  if (child.pid === undefined) throw new Error('spawn 失败');
  return child.pid;
}

/** 杀进程树：Windows 用 taskkill /T（npx→node 子进程链），POSIX 用进程组信号 */
export async function killTree(pid: number): Promise<void> {
  if (process.platform === 'win32') {
    await new Promise<void>((resolve) => {
      const child = spawn('taskkill', ['/PID', String(pid), '/T', '/F'], { stdio: 'ignore' });
      child.on('close', () => resolve());
      child.on('error', () => resolve());
    });
    return;
  }
  try {
    process.kill(-pid, 'SIGTERM');
  } catch {
    try {
      process.kill(pid, 'SIGTERM');
    } catch {
      // 已退出
    }
  }
}

export async function removePidFile(pidPath: string): Promise<void> {
  await fs.rm(pidPath, { force: true }).catch(() => {});
}

export function pidFilePath(homeDir: string): string {
  return path.join(homeDir, 'daemon.pid');
}
