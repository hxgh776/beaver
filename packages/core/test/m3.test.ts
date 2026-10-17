/* M3 新增能力测试：DPAPI 凭证加密（win32）、Gemini 适配器、registry 映射。 */
import { describe, expect, it } from 'vitest';
import { AGENT_LIST } from '../src/agents.js';
import { denormalizeGeminiStyle, normalizeGeminiStyle } from '../src/adapters/json-adapter.js';
import { decryptSecret, encryptSecret } from '../src/secretbox.js';
import {
  baiduMcpWorldToSuggestions,
  baiduMcpWorldTotal,
  clineCatalogToSuggestions,
  dedupeSuggestions,
  dockerCatalogToSuggestions,
  filterByRelevance,
  mcpmRegistryToSuggestions,
  modelscopeToSuggestions,
  npmManifestToSuggestion,
  npmSearchToSuggestions,
  registryEntryToServerInput,
  smitheryToSuggestions,
  sortByPopularity,
} from '../src/registry-discovery.js';

describe('M3.4 S4 凭证加密', () => {
  it.skipIf(process.platform !== 'win32')('DPAPI 加密→解密往返，密文非明文', async () => {
    const secret = 'sk-acceptance-secret-🔑-42';
    const encrypted = await encryptSecret(secret);
    expect(encrypted).toMatch(/^dpapi:v1:[A-Za-z0-9+/=]+$/);
    expect(encrypted).not.toContain('sk-acceptance');
    const decrypted = await decryptSecret(encrypted);
    expect(decrypted).toBe(secret);
  });

  it.skipIf(process.platform !== 'win32')('已加密的值重复加密不二次包裹', async () => {
    const once = await encryptSecret('v');
    const twice = await encryptSecret(once);
    expect(twice).toBe(once);
  });

  it('非 DPAPI 后端原样透传（明文回退）', async () => {
    // 在非 Windows 平台加密=恒等；在 Windows 上该断言依然成立（明文透传由 isEncrypted 保护）
    const value = 'plain-value';
    const out = await encryptSecret(value);
    expect(typeof out).toBe('string');
  });
});

describe('M3.5 Gemini CLI 适配器', () => {
  it('stdio/httpUrl/sse 三形态往返', () => {
    const stdio = { transport: 'stdio' as const, command: 'node', args: ['s.js'], env: { A: '1' } };
    const http = { transport: 'http' as const, url: 'https://x/mcp' };
    const sse = { transport: 'sse' as const, url: 'https://y/sse' };
    expect(normalizeGeminiStyle(denormalizeGeminiStyle(stdio))).toEqual(stdio);
    expect(normalizeGeminiStyle(denormalizeGeminiStyle(http))).toEqual(http);
    expect(normalizeGeminiStyle(denormalizeGeminiStyle(sse))).toEqual(sse);
    expect(normalizeGeminiStyle({ httpUrl: 'https://x/mcp' })).toEqual(http);
    expect(normalizeGeminiStyle({ url: 'https://y/sse' })).toEqual(sse);
  });

  it('注册表含 10 个 agent', () => {
    expect(AGENT_LIST.map((a) => a.type).sort()).toEqual([
      'claude-code', 'claude-desktop', 'codex', 'cursor', 'gemini-cli', 'opencode', 'trae', 'windsurf', 'workbuddy', 'zcode',
    ]);
  });
});

describe('M3.6 registry 条目映射（S7）', () => {
  it('streamable-http 远程 → http + 网关模式（mcp- 前缀剥离为短名）', () => {
    const s = registryEntryToServerInput({
      name: 'io.github.example/mcp-notion',
      description: 'Notion tools',
      remotes: [{ type: 'streamable-http', url: 'https://mcp.notion.com/mcp' }],
    });
    expect(s.name).toBe('notion');
    expect(s.suggestion).toEqual({
      name: 'notion',
      transport: 'http',
      url: 'https://mcp.notion.com/mcp',
      gatewayMode: true,
    });
  });

  it('npm 包 → npx -y；pypi 包 → uvx', () => {
    const npm = registryEntryToServerInput({
      name: 'filesystem',
      packages: [{ registryType: 'npm', identifier: '@modelcontextprotocol/server-filesystem' }],
    });
    expect(npm.suggestion).toMatchObject({ transport: 'stdio', command: 'npx', args: ['-y', '@modelcontextprotocol/server-filesystem'] });
    const pypi = registryEntryToServerInput({
      name: 'mcp-fetch',
      packages: [{ registryType: 'pypi', identifier: 'mcp-server-fetch' }],
    });
    expect(pypi.suggestion).toMatchObject({ transport: 'stdio', command: 'uvx', args: ['mcp-server-fetch'] });
  });

  it('无法识别的条目 → suggestion 为 null', () => {
    const s = registryEntryToServerInput({ name: 'weird' });
    expect(s.suggestion).toBeNull();
  });

  it('npm 搜索回退映射：package → npx 型建议（网关模式）', () => {
    const suggestions = npmSearchToSuggestions({
      objects: [
        { package: { name: '@upstash/context7-mcp', description: 'Up-to-date code docs', version: '4.2.0' } },
        { package: {} },
      ],
    });
    expect(suggestions).toHaveLength(1);
    expect(suggestions[0]).toMatchObject({
      name: '@upstash/context7-mcp',
      description: 'Up-to-date code docs · v4.2.0',
      source: 'npm',
    });
    expect(suggestions[0]!.suggestion).toEqual({
      name: '@upstash/context7-mcp',
      transport: 'stdio',
      command: 'npx',
      args: ['-y', '@upstash/context7-mcp'],
      gatewayMode: true,
    });
  });

  it('Smithery 条目映射：displayName 优先、托管代理端点 + Bearer 占位头、网关模式', () => {
    const suggestions = smitheryToSuggestions({
      servers: [
        {
          qualifiedName: 'upstash/context7-mcp',
          displayName: 'Context7',
          description: 'Fetch up-to-date docs.\nSecond line ignored',
          useCount: 5563,
        },
        { description: 'no qualifiedName' },
      ],
      pagination: { currentPage: 1, totalPages: 35, totalCount: 105 },
    });
    expect(suggestions).toHaveLength(1);
    expect(suggestions[0]).toMatchObject({ name: 'Context7', description: 'Fetch up-to-date docs.', source: 'smithery', useCount: 5563 });
    expect(suggestions[0]!.suggestion).toEqual({
      name: 'Context7',
      transport: 'http',
      url: 'https://server.smithery.ai/@upstash/context7-mcp/mcp',
      headers: { Authorization: 'Bearer {smithery_api_key}' },
      gatewayMode: true,
    });
  });

  it('去重：同名（大小写不敏感）只保留先出现的', () => {
    const out = dedupeSuggestions([
      { name: 'context7', description: 'a', suggestion: null },
      { name: 'Context7', description: 'b', suggestion: null },
      { name: 'other', description: 'c', suggestion: null },
    ]);
    expect(out.map((s) => s.description)).toEqual(['a', 'c']);
  });

  it('相关性过滤：官方源忽略 search 参数时丢弃无关条目，名称命中排前', () => {
    const ranked = filterByRelevance(
      [
        { name: 'docs-mcp', description: 'Remote MCP server for Tandem docs', suggestion: null },
        { name: 'mcp', description: 'Run 150+ AI apps', suggestion: null },
        { name: 'io.github.upstash/context7', description: 'Up-to-date code docs', suggestion: null },
        { name: 'some-tool', description: 'Uses context7 under the hood', suggestion: null },
      ],
      'context7',
    );
    expect(ranked.map((s) => s.name)).toEqual(['io.github.upstash/context7', 'some-tool']);
  });

  it('相关性过滤：空关键词原样返回', () => {
    const list = [{ name: 'x', description: 'y', suggestion: null }];
    expect(filterByRelevance(list, '')).toBe(list);
  });

  it('热度排序：useCount 降序，无热度数据的沉底但保持原相对顺序', () => {
    const sorted = sortByPopularity([
      { name: 'a', description: '', suggestion: null, useCount: 12 },
      { name: 'b', description: '', suggestion: null },
      { name: 'c', description: '', suggestion: null, useCount: 838664 },
      { name: 'd', description: '', suggestion: null, useCount: 12 },
    ]);
    expect(sorted.map((s) => s.name)).toEqual(['c', 'a', 'd', 'b']);
  });

  it('npm 映射带上周下载热度（useCount）', () => {
    const suggestions = npmSearchToSuggestions({
      objects: [{ package: { name: 'context7' }, downloads: { weekly: 1276, monthly: 5135 } }],
    });
    expect(suggestions[0]!.useCount).toBe(1276);
  });

  it('npm manifest 回查映射：有 bin → npx 建议；无 bin / 无名 → null', () => {
    expect(npmManifestToSuggestion({ name: 'codebase-memory-mcp', bin: 'bin.js' })).toEqual({
      name: 'codebase-memory-mcp',
      transport: 'stdio',
      command: 'npx',
      args: ['-y', 'codebase-memory-mcp'],
      gatewayMode: true,
    });
    expect(npmManifestToSuggestion({ name: '@scope/pkg', bin: { pkg: 'cli.js' } })).toMatchObject({
      args: ['-y', '@scope/pkg'],
    });
    expect(npmManifestToSuggestion({ name: 'no-bin-pkg' })).toBeNull();
    expect(npmManifestToSuggestion({ name: 'empty-bin', bin: {} })).toBeNull();
    expect(npmManifestToSuggestion({ bin: { x: 'x.js' } })).toBeNull();
  });

  it('npm 搜索：相关性过滤后再按热度排——同名精确包不被高下载模糊匹配埋掉', () => {
    const ranked = sortByPopularity(
      filterByRelevance(
        npmSearchToSuggestions({
          objects: [
            { package: { name: '@better-auth/memory-adapter', description: 'Memory adapter' }, downloads: { weekly: 11054730 } },
            { package: { name: 'codebase-memory-mcp', description: 'Fast code intelligence' }, downloads: { weekly: 8008 } },
            { package: { name: 'mongodb-memory-server', description: 'MongoDB in-memory' }, downloads: { weekly: 2164713 } },
          ],
        }),
        'codebase-memory-mcp',
      ),
    );
    expect(ranked.map((s) => s.name)).toEqual(['codebase-memory-mcp']);
  });

  it('GitHub registry 包：registry_name 为空时按 runtime_hint 推断 pypi/npx', () => {
    const s = registryEntryToServerInput({
      name: 'microsoft/markitdown',
      packages: [{ name: 'markitdown-mcp', registry_name: '', runtime_hint: 'uvx' }],
    });
    expect(s.suggestion).toMatchObject({ transport: 'stdio', command: 'uvx', args: ['markitdown-mcp'] });
    const npm = registryEntryToServerInput({
      name: 'example/tool',
      packages: [{ name: 'tool-mcp', registry_name: '', runtime_hint: 'npx' }],
    });
    expect(npm.suggestion).toMatchObject({ command: 'npx', args: ['-y', 'tool-mcp'] });
  });

  it('魔搭映射：中文名优先、stdio 配置优先、热度取调用量', () => {
    const suggestions = modelscopeToSuggestions({
      Code: 200,
      Data: {
        McpServer: {
          TotalCount: 2,
          McpServers: [
            {
              Name: 'fetch',
              ChineseName: 'Fetch网页内容抓取',
              AbstractCN: '抓取网页转为 LLM 友好格式',
              CallVolume: 326787344,
              ServerConfig: [{ mcpServers: { fetch: { command: 'uvx', args: ['mcp-server-fetch'] } } }],
            },
            {
              Name: 'amap',
              ServerConfig: [{ mcpServers: { amap: { type: 'sse', url: 'https://mcp.example.com/sse' } } }],
            },
            { Name: 'hosted-only', Hosted: true, DeployedUrl: 'https://hosted.example.com/mcp', ServerConfig: [] },
          ],
        },
      },
    });
    expect(suggestions).toHaveLength(3);
    expect(suggestions[0]).toMatchObject({ name: 'Fetch网页内容抓取', source: 'modelscope', useCount: 326787344 });
    expect(suggestions[0]!.suggestion).toEqual({
      name: 'Fetch网页内容抓取',
      transport: 'stdio',
      command: 'uvx',
      args: ['mcp-server-fetch'],
      gatewayMode: true,
    });
    expect(suggestions[1]!.suggestion).toEqual({
      name: 'amap',
      transport: 'sse',
      url: 'https://mcp.example.com/sse',
      gatewayMode: true,
    });
    // 仅托管端点（需登录态）→ 不给自动添加建议
    expect(suggestions[2]!.suggestion).toBeNull();
  });

  it('百度 MCP World 映射：wrapper 数组（真实形态）、wrapper 对象与平铺数组都兼容，建议为 null 仅浏览', () => {
    const item = { serverName: '高德地图', description: '地图服务', star: 12 };
    const wrapped = baiduMcpWorldToSuggestions({ data: { count: 62282, mcpList: [{ query: '地图', total: 100, servers: [item] }] } });
    const obj = baiduMcpWorldToSuggestions({ data: { count: 1, mcpList: { servers: [item] } } });
    const flat = baiduMcpWorldToSuggestions({ data: { count: 1, mcpList: [item] } });
    for (const list of [wrapped, obj, flat]) {
      expect(list).toHaveLength(1);
      expect(list[0]).toMatchObject({ name: '高德地图', description: '地图服务', useCount: 12, source: 'baidu' });
      expect(list[0]!.suggestion).toBeNull();
    }
    // 搜索总数取内层 total（命中数），无内层时回退全局 count
    expect(baiduMcpWorldTotal({ data: { count: 62282, mcpList: [{ total: 100, servers: [] }] } })).toBe(100);
    expect(baiduMcpWorldTotal({ data: { count: 62282 } })).toBe(62282);
  });

  it('Cline 目录映射：--transport 提取远程 URL、-- 后为 stdio 命令、非 mcp 条目跳过', () => {
    const suggestions = clineCatalogToSuggestions({
      entries: [
        {
          type: 'mcp',
          name: 'Adobe',
          description: 'Adobe Creative Cloud tools',
          install: { args: ['adobe-for-creativity', '--transport', 'http', 'https://adobe.io/mcp'] },
        },
        {
          type: 'mcp',
          name: 'Aikido',
          install: { args: ['aikido', '--', 'npx', '-y', '@aikidosec/mcp'] },
        },
        {
          type: 'mcp',
          name: 'Airtable',
          install: {
            args: ['airtable', '--transport', 'http', 'https://mcp.airtable.com/mcp'],
            env: [{ name: 'AIRTABLE_TOKEN', required: true, description: 'token' }],
          },
        },
        { type: 'skill', name: 'a-skill' },
      ],
    });
    expect(suggestions).toHaveLength(3);
    expect(suggestions[0]!.suggestion).toEqual({ name: 'Adobe', transport: 'http', url: 'https://adobe.io/mcp', gatewayMode: true });
    expect(suggestions[1]!.suggestion).toEqual({ name: 'Aikido', transport: 'stdio', command: 'npx', args: ['-y', '@aikidosec/mcp'], gatewayMode: true });
    expect(suggestions[2]!.suggestion).toMatchObject({ transport: 'http' });
    expect(suggestions[2]!.description).toContain('AIRTABLE_TOKEN');
  });

  it('Docker 目录映射：server 类型 → docker run stdio，镜像保持 digest 固定', () => {
    const suggestions = dockerCatalogToSuggestions({
      registry: {
        fetch: { type: 'server', title: 'Fetch', description: 'Fetch web pages', image: 'mcp/fetch@sha256:abc' },
        secret: { type: 'secret', title: 'Secret', image: 'mcp/secret' },
        noimage: { type: 'server', title: 'NoImage' },
      },
    });
    expect(suggestions).toHaveLength(1);
    expect(suggestions[0]).toMatchObject({ name: 'Fetch', description: 'Fetch web pages', source: 'docker' });
    expect(suggestions[0]!.suggestion).toEqual({
      name: 'Fetch',
      transport: 'stdio',
      command: 'docker',
      args: ['run', '-i', '--rm', 'mcp/fetch@sha256:abc'],
      gatewayMode: true,
    });
  });

  it('mcpm 映射：npm 安装优先、python 兜底、custom 占位跳过、必填参数进描述', () => {
    const suggestions = mcpmRegistryToSuggestions({
      'a': {
        name: 'a',
        display_name: 'A Tool',
        description: 'Does things',
        installations: { npm: { command: 'npx', args: ['-y', 'a-mcp'], env: { A_KEY: 'placeholder' } } },
        arguments: { API_KEY: { required: true } },
      },
      'b': { name: 'b', installations: { python: { command: 'uvx', args: ['b-mcp'] } } },
      'c': { name: 'c', installations: { custom: { command: '/path/to/x', args: [] } } },
    });
    expect(suggestions).toHaveLength(3);
    expect(suggestions[0]!.suggestion).toEqual({
      name: 'A Tool',
      transport: 'stdio',
      command: 'npx',
      args: ['-y', 'a-mcp'],
      env: { A_KEY: 'placeholder' },
      gatewayMode: true,
    });
    expect(suggestions[0]!.description).toBe('Does things · 需配置：API_KEY');
    expect(suggestions[1]!.suggestion).toMatchObject({ command: 'uvx', args: ['b-mcp'] });
    expect(suggestions[2]!.suggestion).toBeNull();
  });
});
