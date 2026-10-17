import os from 'node:os';
import path from 'node:path';
import { CodexTomlAdapter } from './adapters/codex-toml.js';
import {
  JsonAgentAdapter,
  denormalizeClaudeStyle,
  denormalizeOpencode,
  denormalizeUrlOrCommand,
  denormalizeWorkbuddy,
  normalizeClaudeStyle,
  normalizeGeminiStyle,
  normalizeOpencode,
  normalizeUrlOrCommand,
  normalizeWorkbuddy,
  denormalizeGeminiStyle,
} from './adapters/json-adapter.js';
import type { AgentAdapter } from './adapters/types.js';
import type { AgentType } from './types.js';

export interface AgentDefinition {
  type: AgentType;
  displayName: string;
  adapter: AgentAdapter;
}

/**
 * 构建全部 agent 定义。baseDir 缺省为用户主目录；
 * 测试传入沙箱目录即可获得与生产完全一致的适配器组合。
 */
export function makeAgents(baseDir?: string): Record<AgentType, AgentDefinition> {
  const p = (...segments: string[]): string => path.join(baseDir ?? os.homedir(), ...segments);

  const claudeCode = new JsonAgentAdapter({
    type: 'claude-code',
    displayName: 'Claude Code',
    transports: ['stdio', 'http', 'sse'],
    paths: [p('.claude.json')],
    containerKey: 'mcpServers',
    normalize: normalizeClaudeStyle,
    denormalize: denormalizeClaudeStyle,
  });

  const codex = new CodexTomlAdapter([p('.codex', 'config.toml')]);

  const cursor = new JsonAgentAdapter({
    type: 'cursor',
    displayName: 'Cursor',
    transports: ['stdio', 'http'],
    paths: [p('.cursor', 'mcp.json')],
    containerKey: 'mcpServers',
    normalize: normalizeUrlOrCommand,
    denormalize: denormalizeUrlOrCommand,
  });

  const trae = new JsonAgentAdapter({
    type: 'trae',
    displayName: 'Trae',
    transports: ['stdio', 'http'],
    paths: [p('.trae', 'mcp.json'), p('.trae-cn', 'mcp.json')],
    containerKey: 'mcpServers',
    normalize: normalizeUrlOrCommand,
    denormalize: denormalizeUrlOrCommand,
  });

  const opencode = new JsonAgentAdapter({
    type: 'opencode',
    displayName: 'OpenCode',
    transports: ['stdio', 'http'],
    paths: [p('.config', 'opencode', 'opencode.json')],
    containerKey: 'mcp',
    normalize: normalizeOpencode,
    denormalize: denormalizeOpencode,
  });

  // M3.5：Claude Desktop（路径随平台不同）
  const claudeDesktopConfig =
    process.platform === 'win32'
      ? path.join(process.env['APPDATA'] ?? p('AppData', 'Roaming'), 'Claude', 'claude_desktop_config.json')
      : process.platform === 'darwin'
        ? p('Library', 'Application Support', 'Claude', 'claude_desktop_config.json')
        : p('.config', 'Claude', 'claude_desktop_config.json');
  const claudeDesktop = new JsonAgentAdapter({
    type: 'claude-desktop',
    displayName: 'Claude Desktop',
    transports: ['stdio', 'http', 'sse'],
    paths: [claudeDesktopConfig],
    containerKey: 'mcpServers',
    normalize: normalizeClaudeStyle,
    denormalize: denormalizeClaudeStyle,
  });

  const windsurf = new JsonAgentAdapter({
    type: 'windsurf',
    displayName: 'Windsurf',
    transports: ['stdio', 'http'],
    paths: [p('.codeium', 'windsurf', 'mcp_config.json')],
    containerKey: 'mcpServers',
    normalize: normalizeUrlOrCommand,
    denormalize: denormalizeUrlOrCommand,
  });

  const geminiCli = new JsonAgentAdapter({
    type: 'gemini-cli',
    displayName: 'Gemini CLI',
    transports: ['stdio', 'http'],
    paths: [p('.gemini', 'settings.json')],
    containerKey: 'mcpServers',
    normalize: normalizeGeminiStyle,
    denormalize: denormalizeGeminiStyle,
  });

  // ZCode：用户级 MCP 在 ~/.zcode/cli/config.json 的 mcp.servers（嵌套容器键）。
  // schema 严格——未知键会被客户端丢弃，因此写入只用官方字段（command/args/cwd/env/headers 等）。
  const zcode = new JsonAgentAdapter({
    type: 'zcode',
    displayName: 'ZCode',
    transports: ['stdio', 'http', 'sse'],
    paths: [p('.zcode', 'cli', 'config.json')],
    containerKey: 'mcp.servers',
    normalize: normalizeClaudeStyle,
    denormalize: denormalizeClaudeStyle,
  });

  // WorkBuddy：~/.workbuddy/mcp.json，顶层 mcpServers；远程条目 type 为 streamableHttp
  const workbuddy = new JsonAgentAdapter({
    type: 'workbuddy',
    displayName: 'WorkBuddy',
    transports: ['stdio', 'http'],
    paths: [p('.workbuddy', 'mcp.json')],
    containerKey: 'mcpServers',
    normalize: normalizeWorkbuddy,
    denormalize: denormalizeWorkbuddy,
  });

  return {
    'claude-code': { type: 'claude-code', displayName: 'Claude Code', adapter: claudeCode },
    codex: { type: 'codex', displayName: 'Codex CLI', adapter: codex },
    cursor: { type: 'cursor', displayName: 'Cursor', adapter: cursor },
    trae: { type: 'trae', displayName: 'Trae', adapter: trae },
    opencode: { type: 'opencode', displayName: 'OpenCode', adapter: opencode },
    'claude-desktop': { type: 'claude-desktop', displayName: 'Claude Desktop', adapter: claudeDesktop },
    windsurf: { type: 'windsurf', displayName: 'Windsurf', adapter: windsurf },
    'gemini-cli': { type: 'gemini-cli', displayName: 'Gemini CLI', adapter: geminiCli },
    zcode: { type: 'zcode', displayName: 'ZCode', adapter: zcode },
    workbuddy: { type: 'workbuddy', displayName: 'WorkBuddy', adapter: workbuddy },
  };
}

export const AGENTS: Record<AgentType, AgentDefinition> = makeAgents();
export const AGENT_LIST: AgentDefinition[] = Object.values(AGENTS);
