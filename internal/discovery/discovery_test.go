package discovery

// 九源条目映射测试（移植自 TS packages/core/test/m3.test.ts 的 M3.6 registry 段）。

import (
	"encoding/json"
	"testing"

)

func m(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("测试 JSON 非法: %v", err)
	}
	return out
}

func mj(t *testing.T, s string) any {
	t.Helper()
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("测试 JSON 非法: %v", err)
	}
	return out
}

// streamable-http 远程 → http + 网关模式（mcp- 前缀剥离为短名）
func TestRegistryEntryRemoteHTTP(t *testing.T) {
	s := RegistryEntryToServerInput(m(t, `{
		"name": "io.github.example/mcp-notion",
		"description": "Notion tools",
		"remotes": [{"type": "streamable-http", "url": "https://mcp.notion.com/mcp"}]
	}`))
	if s.Name != "notion" {
		t.Fatalf("短名应为 notion，得到 %s", s.Name)
	}
	if s.Suggestion == nil || s.Suggestion.Transport != "http" || s.Suggestion.URL == nil ||
		*s.Suggestion.URL != "https://mcp.notion.com/mcp" || s.Suggestion.GatewayMode == nil || !*s.Suggestion.GatewayMode {
		t.Fatalf("suggestion 不符: %+v", s.Suggestion)
	}
}

// npm 包 → npx -y；pypi 包 → uvx
func TestRegistryEntryPackages(t *testing.T) {
	npm := RegistryEntryToServerInput(m(t, `{
		"name": "filesystem",
		"packages": [{"registryType": "npm", "identifier": "@modelcontextprotocol/server-filesystem"}]
	}`))
	if npm.Suggestion.Command == nil || *npm.Suggestion.Command != "npx" || npm.Suggestion.Args == nil || (*npm.Suggestion.Args)[0] != "-y" {
		t.Fatalf("npm 包应映射 npx -y: %+v", npm.Suggestion)
	}
	pypi := RegistryEntryToServerInput(m(t, `{
		"name": "mcp-fetch",
		"packages": [{"registryType": "pypi", "identifier": "mcp-server-fetch"}]
	}`))
	if pypi.Suggestion.Command == nil || *pypi.Suggestion.Command != "uvx" {
		t.Fatalf("pypi 包应映射 uvx: %+v", pypi.Suggestion)
	}
}

// 无法识别的条目 → suggestion 为 nil
func TestRegistryEntryUnknown(t *testing.T) {
	s := RegistryEntryToServerInput(m(t, `{"name": "weird"}`))
	if s.Suggestion != nil {
		t.Fatalf("无法识别应 nil: %+v", s.Suggestion)
	}
}

// GitHub registry 包：registry_name 为空时按 runtime_hint 推断 pypi/npx
func TestRegistryEntryRuntimeHint(t *testing.T) {
	s := RegistryEntryToServerInput(m(t, `{
		"name": "microsoft/markitdown",
		"packages": [{"name": "markitdown-mcp", "registry_name": "", "runtime_hint": "uvx"}]
	}`))
	if s.Suggestion.Command == nil || *s.Suggestion.Command != "uvx" {
		t.Fatalf("runtime_hint uvx 应推断 pypi: %+v", s.Suggestion)
	}
	npm := RegistryEntryToServerInput(m(t, `{
		"name": "example/tool",
		"packages": [{"name": "tool-mcp", "registry_name": "", "runtime_hint": "npx"}]
	}`))
	if npm.Suggestion.Command == nil || *npm.Suggestion.Command != "npx" {
		t.Fatalf("runtime_hint npx 应映射 npx: %+v", npm.Suggestion)
	}
}

// npm 搜索回退映射：package → npx 型建议（网关模式）+ 周下载热度
func TestNpmSearchToSuggestions(t *testing.T) {
	suggestions := NpmSearchToSuggestions(m(t, `{
		"objects": [
			{"package": {"name": "@upstash/context7-mcp", "description": "Up-to-date code docs", "version": "4.2.0"}},
			{"package": {}}
		]
	}`))
	if len(suggestions) != 1 {
		t.Fatalf("应 1 条，得到 %d", len(suggestions))
	}
	s := suggestions[0]
	if s.Name != "@upstash/context7-mcp" || s.Description != "Up-to-date code docs · v4.2.0" || s.Source != "npm" {
		t.Fatalf("映射不符: %+v", s)
	}
	if s.Suggestion == nil || s.Suggestion.Command == nil || *s.Suggestion.Command != "npx" || !*s.Suggestion.GatewayMode {
		t.Fatalf("npx 建议不符: %+v", s.Suggestion)
	}
	// 周下载热度
	withDownloads := NpmSearchToSuggestions(m(t, `{"objects": [{"package": {"name": "context7"}, "downloads": {"weekly": 1276, "monthly": 5135}}]}`))
	if withDownloads[0].UseCount == nil || *withDownloads[0].UseCount != 1276 {
		t.Fatalf("useCount 应为 1276: %+v", withDownloads[0].UseCount)
	}
}

// npm manifest 回查映射：有 bin → npx 建议；无 bin / 无名 → nil
func TestNpmManifestToSuggestion(t *testing.T) {
	got := NpmManifestToSuggestion(m(t, `{"name": "codebase-memory-mcp", "bin": "bin.js"}`))
	if got == nil || got.Command == nil || *got.Command != "npx" {
		t.Fatalf("有 bin 应 npx 建议: %+v", got)
	}
	scopeGot := NpmManifestToSuggestion(m(t, `{"name": "@scope/pkg", "bin": {"pkg": "cli.js"}}`))
	if scopeGot == nil {
		t.Fatalf("对象 bin 应给建议")
	}
	if NpmManifestToSuggestion(m(t, `{"name": "no-bin-pkg"}`)) != nil {
		t.Fatalf("无 bin 应 nil")
	}
	if NpmManifestToSuggestion(m(t, `{"name": "empty-bin", "bin": {}}`)) != nil {
		t.Fatalf("空对象 bin 应 nil")
	}
	if NpmManifestToSuggestion(m(t, `{"bin": {"x": "x.js"}}`)) != nil {
		t.Fatalf("无名应 nil")
	}
}

// Smithery 条目映射：displayName 优先、托管代理端点 + Bearer 占位头、网关模式
func TestSmitheryToSuggestions(t *testing.T) {
	suggestions := SmitheryToSuggestions(m(t, `{
		"servers": [
			{"qualifiedName": "upstash/context7-mcp", "displayName": "Context7", "description": "Fetch up-to-date docs.\nSecond line ignored", "useCount": 5563},
			{"description": "no qualifiedName"}
		],
		"pagination": {"currentPage": 1, "totalPages": 35, "totalCount": 105}
	}`))
	if len(suggestions) != 1 {
		t.Fatalf("应 1 条，得到 %d", len(suggestions))
	}
	s := suggestions[0]
	if s.Name != "Context7" || s.Description != "Fetch up-to-date docs." || s.Source != "smithery" || s.UseCount == nil || *s.UseCount != 5563 {
		t.Fatalf("映射不符: %+v", s)
	}
	if s.Suggestion.URL == nil || *s.Suggestion.URL != "https://server.smithery.ai/@upstash/context7-mcp/mcp" {
		t.Fatalf("端点不符: %+v", s.Suggestion)
	}
	if s.Suggestion.Headers == nil || (*s.Suggestion.Headers)["Authorization"] != "Bearer {smithery_api_key}" {
		t.Fatalf("占位头不符: %+v", s.Suggestion.Headers)
	}
	if !*s.Suggestion.GatewayMode {
		t.Fatalf("应为网关模式")
	}
}

// 魔搭映射：中文名优先、stdio 配置优先、热度取调用量、仅托管不给建议
func TestModelscopeToSuggestions(t *testing.T) {
	suggestions := ModelscopeToSuggestions(m(t, `{
		"Code": 200,
		"Data": {"McpServer": {"TotalCount": 2, "McpServers": [
			{
				"Name": "fetch", "ChineseName": "Fetch网页内容抓取",
				"AbstractCN": "抓取网页转为 LLM 友好格式", "CallVolume": 326787344,
				"ServerConfig": [{"mcpServers": {"fetch": {"command": "uvx", "args": ["mcp-server-fetch"]}}}]
			},
			{
				"Name": "amap",
				"ServerConfig": [{"mcpServers": {"amap": {"type": "sse", "url": "https://mcp.example.com/sse"}}}]
			},
			{"Name": "hosted-only", "Hosted": true, "DeployedUrl": "https://hosted.example.com/mcp", "ServerConfig": []}
		]}}
	}`))
	if len(suggestions) != 3 {
		t.Fatalf("应 3 条，得到 %d", len(suggestions))
	}
	first := suggestions[0]
	if first.Name != "Fetch网页内容抓取" || first.Source != "modelscope" || first.UseCount == nil || *first.UseCount != 326787344 {
		t.Fatalf("第一条不符: %+v", first)
	}
	if first.Suggestion.Command == nil || *first.Suggestion.Command != "uvx" || !*first.Suggestion.GatewayMode {
		t.Fatalf("stdio 建议不符: %+v", first.Suggestion)
	}
	second := suggestions[1].Suggestion
	if second == nil || second.Transport != "sse" || second.URL == nil || *second.URL != "https://mcp.example.com/sse" {
		t.Fatalf("sse 建议不符: %+v", second)
	}
	// 仅托管端点（需登录态）→ 不给自动添加建议
	if suggestions[2].Suggestion != nil {
		t.Fatalf("hosted-only 应无建议: %+v", suggestions[2].Suggestion)
	}
}

// 百度 MCP World 映射：wrapper 数组（真实形态）、wrapper 对象与平铺数组都兼容，建议为 nil 仅浏览
func TestBaiduMcpWorld(t *testing.T) {
	item := `{"serverName": "高德地图", "description": "地图服务", "star": 12}`
	for name, data := range map[string]string{
		"wrapped": `{"data": {"count": 62282, "mcpList": [{"query": "地图", "total": 100, "servers": [` + item + `]}]}}`,
		"obj":     `{"data": {"count": 1, "mcpList": {"servers": [` + item + `]}}}`,
		"flat":    `{"data": {"count": 1, "mcpList": [` + item + `]}}`,
	} {
		list := BaiduMcpWorldToSuggestions(m(t, data))
		if len(list) != 1 {
			t.Fatalf("%s: 应 1 条，得到 %d", name, len(list))
		}
		if list[0].Name != "高德地图" || list[0].Description != "地图服务" || list[0].Source != "baidu" {
			t.Fatalf("%s: 映射不符: %+v", name, list[0])
		}
		if list[0].UseCount == nil || *list[0].UseCount != 12 {
			t.Fatalf("%s: star 热度不符", name)
		}
		if list[0].Suggestion != nil {
			t.Fatalf("%s: 建议应为 nil", name)
		}
	}
	// 搜索总数取内层 total（命中数），无内层时回退全局 count
	if total := BaiduMcpWorldTotal(m(t, `{"data": {"count": 62282, "mcpList": [{"total": 100, "servers": []}]}}`)); total == nil || *total != 100 {
		t.Fatalf("内层 total 应优先")
	}
	if total := BaiduMcpWorldTotal(m(t, `{"data": {"count": 62282}}`)); total == nil || *total != 62282 {
		t.Fatalf("无内层应回退 count")
	}
}

// Cline 目录映射：--transport 提取远程 URL、-- 后为 stdio 命令、非 mcp 条目跳过
func TestClineCatalogToSuggestions(t *testing.T) {
	suggestions := ClineCatalogToSuggestions(m(t, `{
		"entries": [
			{"type": "mcp", "name": "Adobe", "description": "Adobe Creative Cloud tools",
			 "install": {"args": ["adobe-for-creativity", "--transport", "http", "https://adobe.io/mcp"]}},
			{"type": "mcp", "name": "Aikido", "install": {"args": ["aikido", "--", "npx", "-y", "@aikidosec/mcp"]}},
			{"type": "mcp", "name": "Airtable",
			 "install": {"args": ["airtable", "--transport", "http", "https://mcp.airtable.com/mcp"],
			             "env": [{"name": "AIRTABLE_TOKEN", "required": true, "description": "token"}]}},
			{"type": "skill", "name": "a-skill"}
		]
	}`))
	if len(suggestions) != 3 {
		t.Fatalf("应 3 条（非 mcp 跳过），得到 %d", len(suggestions))
	}
	if suggestions[0].Suggestion.URL == nil || *suggestions[0].Suggestion.URL != "https://adobe.io/mcp" {
		t.Fatalf("Adobe 远程端点不符: %+v", suggestions[0].Suggestion)
	}
	aikido := suggestions[1].Suggestion
	if aikido.Command == nil || *aikido.Command != "npx" || aikido.Args == nil || (*aikido.Args)[1] != "@aikidosec/mcp" {
		t.Fatalf("Aikido stdio 不符: %+v", aikido)
	}
	if suggestions[2].Suggestion.Transport != "http" {
		t.Fatalf("Airtable 应 http: %+v", suggestions[2].Suggestion)
	}
	if !contains(suggestions[2].Description, "AIRTABLE_TOKEN") {
		t.Fatalf("必填 env 应进描述: %q", suggestions[2].Description)
	}
}

// Docker 目录映射：server 类型 → docker run stdio，镜像保持 digest 固定
func TestDockerCatalogToSuggestions(t *testing.T) {
	suggestions := DockerCatalogToSuggestions([]byte(`{
		"registry": {
			"fetch": {"type": "server", "title": "Fetch", "description": "Fetch web pages", "image": "mcp/fetch@sha256:abc"},
			"secret": {"type": "secret", "title": "Secret", "image": "mcp/secret"},
			"noimage": {"type": "server", "title": "NoImage"}
		}
	}`))
	if len(suggestions) != 1 {
		t.Fatalf("应 1 条，得到 %d", len(suggestions))
	}
	if suggestions[0].Name != "Fetch" || suggestions[0].Description != "Fetch web pages" || suggestions[0].Source != "docker" {
		t.Fatalf("映射不符: %+v", suggestions[0])
	}
	want := []string{"run", "-i", "--rm", "mcp/fetch@sha256:abc"}
	got := *suggestions[0].Suggestion.Args
	if len(got) != len(want) || got[3] != want[3] {
		t.Fatalf("docker run 参数不符: %v", got)
	}
}

// mcpm 映射：npm 安装优先、python 兜底、custom 占位跳过、必填参数进描述
func TestMcpmRegistryToSuggestions(t *testing.T) {
	suggestions := McpmRegistryToSuggestions([]byte(`{
		"a": {"name": "a", "display_name": "A Tool", "description": "Does things",
		      "installations": {"npm": {"command": "npx", "args": ["-y", "a-mcp"], "env": {"A_KEY": "placeholder"}}},
		      "arguments": {"API_KEY": {"required": true}}},
		"b": {"name": "b", "installations": {"python": {"command": "uvx", "args": ["b-mcp"]}}},
		"c": {"name": "c", "installations": {"custom": {"command": "/path/to/x", "args": []}}}
	}`))
	if len(suggestions) != 3 {
		t.Fatalf("应 3 条，得到 %d", len(suggestions))
	}
	a := suggestions[0]
	if a.Name != "A Tool" || a.Description != "Does things · 需配置：API_KEY" {
		t.Fatalf("A 映射不符: %+v", a)
	}
	if a.Suggestion.Env == nil || (*a.Suggestion.Env)["A_KEY"] != "placeholder" {
		t.Fatalf("env 应透传: %+v", a.Suggestion.Env)
	}
	b := suggestions[1].Suggestion
	if b.Command == nil || *b.Command != "uvx" {
		t.Fatalf("python 兜底不符: %+v", b)
	}
	if suggestions[2].Suggestion != nil {
		t.Fatalf("custom 占位应无建议: %+v", suggestions[2].Suggestion)
	}
}

// 去重：同名（大小写不敏感）只保留先出现的
func TestDedupe(t *testing.T) {
	out := Dedupe([]Suggestion{
		{Name: "context7", Description: "a"},
		{Name: "Context7", Description: "b"},
		{Name: "other", Description: "c"},
	})
	if len(out) != 2 || out[0].Description != "a" || out[1].Description != "c" {
		t.Fatalf("去重不符: %+v", out)
	}
}

// 相关性过滤：官方源忽略 search 参数时丢弃无关条目，名称命中排前
func TestFilterByRelevance(t *testing.T) {
	ranked := FilterByRelevance([]Suggestion{
		{Name: "docs-mcp", Description: "Remote MCP server for Tandem docs"},
		{Name: "mcp", Description: "Run 150+ AI apps"},
		{Name: "io.github.upstash/context7", Description: "Up-to-date code docs"},
		{Name: "some-tool", Description: "Uses context7 under the hood"},
	}, "context7")
	if len(ranked) != 2 || ranked[0].Name != "io.github.upstash/context7" || ranked[1].Name != "some-tool" {
		t.Fatalf("排序不符: %+v", rankedNames(ranked))
	}
	// 空关键词原样返回
	list := []Suggestion{{Name: "x", Description: "y"}}
	if got := FilterByRelevance(list, ""); len(got) != 1 || got[0].Name != "x" {
		t.Fatalf("空关键词应原样返回")
	}
}

// 热度排序：useCount 降序，无热度数据的沉底但保持原相对顺序
func TestSortByPopularity(t *testing.T) {
	sorted := SortByPopularity([]Suggestion{
		{Name: "a", UseCount: numPtr(12)},
		{Name: "b"},
		{Name: "c", UseCount: numPtr(838664)},
		{Name: "d", UseCount: numPtr(12)},
	})
	want := []string{"c", "a", "d", "b"}
	got := rankedNames(sorted)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("排序不符: %v", got)
		}
	}
}

// npm 搜索：相关性过滤后再按热度排——同名精确包不被高下载模糊匹配埋掉
func TestNpmRelevanceOverPopularity(t *testing.T) {
	ranked := SortByPopularity(FilterByRelevance(NpmSearchToSuggestions(m(t, `{
		"objects": [
			{"package": {"name": "@better-auth/memory-adapter", "description": "Memory adapter"}, "downloads": {"weekly": 11054730}},
			{"package": {"name": "codebase-memory-mcp", "description": "Fast code intelligence"}, "downloads": {"weekly": 8008}},
			{"package": {"name": "mongodb-memory-server", "description": "MongoDB in-memory"}, "downloads": {"weekly": 2164713}}
		]
	}`)), "codebase-memory-mcp"))
	if len(ranked) != 1 || ranked[0].Name != "codebase-memory-mcp" {
		t.Fatalf("精确命中应唯一存活: %+v", rankedNames(ranked))
	}
}

func rankedNames(list []Suggestion) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.Name)
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexStr(s, sub) >= 0)
}

func indexStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func numPtr(f float64) *float64 { return &f }
