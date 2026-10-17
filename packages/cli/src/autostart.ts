// 开机自启：登录时静默拉起 `dam start`（start 本身幂等，已在运行则直接返回）。
// Windows：HKCU Run 键 + wscript 隐藏窗口包装（用户级，免管理员）；
// macOS：~/Library/LaunchAgents 的 LaunchAgent（RunAtLoad）；
// Linux：systemd user unit（default.target）。
// 自启条目固定指向当前 CLI 入口与 node 可执行文件的绝对路径；CLI 挪窝/重装后重跑 `dam autostart on` 即可。
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { homedir, platform } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const WIN_RUN_KEY = 'HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run';
const WIN_VALUE_NAME = 'Beaver';
const MAC_LABEL = 'ai.beaver.daemon';
const LINUX_UNIT_NAME = 'beaver.service';

/** CLI 真实入口（npm 全局 bin 符号链接解析到包内 dist/index.js） */
function cliEntry(): string {
  const raw = process.argv[1] ?? fileURLToPath(import.meta.url);
  try {
    return realpathSync(raw);
  } catch {
    return path.resolve(raw);
  }
}

function startCommandLine(homeOverride?: string): string {
  const homeArgs = homeOverride ? ` --home "${homeOverride}"` : '';
  return `"${process.execPath}" "${cliEntry()}" start${homeArgs}`;
}

function vbsPath(): string {
  return path.join(path.dirname(cliEntry()), 'beaver-autostart.vbs');
}

function macPlistPath(): string {
  return path.join(homedir(), 'Library', 'LaunchAgents', `${MAC_LABEL}.plist`);
}

function linuxUnitPath(): string {
  return path.join(homedir(), '.config', 'systemd', 'user', LINUX_UNIT_NAME);
}

function run(cmd: string, args: string[], quiet = false): { ok: boolean; output: string } {
  const res = spawnSync(cmd, args, { encoding: 'utf8', windowsHide: true });
  if (!quiet && res.error) throw res.error;
  return { ok: !res.status, output: `${res.stdout ?? ''}${res.stderr ?? ''}`.trim() };
}

function escapeXml(s: string): string {
  return s.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
}

type Platform = 'windows' | 'macos' | 'linux';
function currentPlatform(): Platform {
  if (platform() === 'win32') return 'windows';
  if (platform() === 'darwin') return 'macos';
  if (platform() === 'linux') return 'linux';
  throw new Error(`暂不支持的平台: ${platform()}（自启支持 Windows / macOS / Linux）`);
}

function installWindows(homeOverride?: string): string {
  // VBS 以隐藏窗口（0）启动 start 命令；On Error 抑制 wscript 弹窗（如 CLI 被移动的瞬间）
  const cmdLine = startCommandLine(homeOverride);
  const vbs = `On Error Resume Next\r\nCreateObject("WScript.Shell").Run "${cmdLine.replaceAll('"', '""')}", 0, False\r\n`;
  writeFileSync(vbsPath(), vbs, 'utf8');
  run('reg', ['add', WIN_RUN_KEY, '/v', WIN_VALUE_NAME, '/t', 'REG_SZ', '/d', `wscript.exe "${vbsPath()}"`, '/f']);
  return cmdLine;
}

function uninstallWindows(): void {
  run('reg', ['delete', WIN_RUN_KEY, '/v', WIN_VALUE_NAME, '/f'], true);
  rmSync(vbsPath(), { force: true });
}

function isInstalledWindows(): boolean {
  return run('reg', ['query', WIN_RUN_KEY, '/v', WIN_VALUE_NAME], true).ok;
}

function installMacos(homeOverride?: string): string {
  const plistPath = macPlistPath();
  mkdirSync(path.dirname(plistPath), { recursive: true });
  const args = [process.execPath, cliEntry(), 'start', ...(homeOverride ? ['--home', homeOverride] : [])];
  const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key><string>${MAC_LABEL}</string>
    <key>ProgramArguments</key>
    <array>${args.map((a) => `\n      <string>${escapeXml(a)}</string>`).join('')}\n    </array>
    <key>RunAtLoad</key><true/>
  </dict>
</plist>
`;
  writeFileSync(plistPath, plist, 'utf8');
  run('launchctl', ['unload', plistPath], true); // 幂等重装
  run('launchctl', ['load', plistPath]);
  return args.join(' ');
}

function uninstallMacos(): void {
  const plistPath = macPlistPath();
  if (existsSync(plistPath)) run('launchctl', ['unload', plistPath], true);
  rmSync(plistPath, { force: true });
}

function installLinux(homeOverride?: string): string {
  const unitPath = linuxUnitPath();
  mkdirSync(path.dirname(unitPath), { recursive: true });
  const exec = [process.execPath, cliEntry(), 'start', ...(homeOverride ? ['--home', homeOverride] : [])]
    .map((a) => `"${a}"`)
    .join(' ');
  const unit = `[Unit]
Description=Beaver daemon (dam start)

[Service]
Type=oneshot
ExecStart=${exec}

[Install]
WantedBy=default.target
`;
  writeFileSync(unitPath, unit, 'utf8');
  run('systemctl', ['--user', 'daemon-reload'], true);
  run('systemctl', ['--user', 'enable', LINUX_UNIT_NAME]);
  return exec;
}

function uninstallLinux(): void {
  run('systemctl', ['--user', 'disable', LINUX_UNIT_NAME], true);
  rmSync(linuxUnitPath(), { force: true });
  run('systemctl', ['--user', 'daemon-reload'], true);
}

/** @returns 安装后写入自启条目的命令行（用于回显） */
export function autostartInstall(homeOverride?: string): string {
  const p = currentPlatform();
  if (p === 'windows') return installWindows(homeOverride);
  if (p === 'macos') return installMacos(homeOverride);
  return installLinux(homeOverride);
}

export function autostartUninstall(): void {
  const p = currentPlatform();
  if (p === 'windows') uninstallWindows();
  else if (p === 'macos') uninstallMacos();
  else uninstallLinux();
}

export function autostartIsInstalled(): boolean {
  const p = currentPlatform();
  if (p === 'windows') return isInstalledWindows();
  if (p === 'macos') return existsSync(macPlistPath());
  return existsSync(linuxUnitPath());
}
