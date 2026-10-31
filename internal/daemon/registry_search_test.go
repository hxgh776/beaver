package daemon

// 九源搜索路由测试（移植自 TS api.test.ts 的 registry 搜索段）。
// 上游用 httptest 桩替换（官方 registry / npm manifest / npm 搜索）。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubUpstreams 覆盖上游地址 → 桩服务器；返回恢复函数。
func stubUpstreams(t *testing.T, routes map[string]stubRoute) func() {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for prefix, route := range routes {
			if strings.HasPrefix(r.URL.Path, prefix) || strings.Contains(r.URL.RawQuery, prefix) {
				status := route.status
				if status == 0 {
					status = http.StatusOK
				}
				w.Header().Set("content-type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(route.body))
				return
			}
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(ts.Close)
	old := map[string]*string{
		"official":   &officialRegistry,
		"github":     &githubRegistry,
		"smithery":   &smitheryRegistry,
		"npm0":       &npmHostOfficial,
		"npm1":       &npmHostMirror,
		"modelscope": &modelscopeAPI,
		"baidu":      &baiduMcpAPI,
		"cline":      &clineCatalog,
		"docker":     &dockerCatalog,
		"mcpm":       &mcpmRegistry,
	}
	// 全部上游都指向同一桩（桩内按路径区分）
	officialRegistry = ts.URL
	githubRegistry = ts.URL
	smitheryRegistry = ts.URL
	npmHostOfficial = ts.URL
	npmHostMirror = ts.URL
	modelscopeAPI = ts.URL
	baiduMcpAPI = ts.URL
	clineCatalog = ts.URL
	dockerCatalog = ts.URL
	mcpmRegistry = ts.URL
	// 目录缓存清空（避免跨测试污染）
	catalogCacheMu.Lock()
	catalogCache = map[string]catalogEntry{}
	catalogCacheMu.Unlock()
	return func() {
		officialRegistry = *old["official"]
		githubRegistry = *old["github"]
		smitheryRegistry = *old["smithery"]
		npmHostOfficial = *old["npm0"]
		npmHostMirror = *old["npm1"]
		modelscopeAPI = *old["modelscope"]
		baiduMcpAPI = *old["baidu"]
		clineCatalog = *old["cline"]
		dockerCatalog = *old["docker"]
		mcpmRegistry = *old["mcpm"]
	}
}

type stubRoute struct {
	body   string
	status int
}

// 官方条目仅 mcpb 包 → 按名回查 npm 补 npx 建议，描述附回退说明
func TestRegistrySearchMcpbBackfill(t *testing.T) {
	h := newAPIHarness(t)
	restore := stubUpstreams(t, map[string]stubRoute{
		"/v0/servers": {body: `{
			"servers": [{"server": {
				"name": "io.github.DeusData/codebase-memory-mcp",
				"title": "Codebase Memory",
				"description": "Persistent codebase knowledge graph.",
				"version": "0.1.3",
				"packages": [{"registryType": "mcpb", "identifier": "https://github.com/x/y/releases/download/v0.1.3/y-win.zip", "transport": {"type": "stdio"}}]
			}}],
			"metadata": {"total": 1}
		}`},
		"/codebase-memory-mcp/latest": {body: `{"name": "codebase-memory-mcp", "bin": {"codebase-memory-mcp": "bin.js"}}`},
	})
	defer restore()

	status, data := h.api("GET", "/api/registry/search?q=codebase-memory-mcp&limit=20&offset=0&source=official", nil)
	if status != http.StatusOK {
		t.Fatalf("应 200: %d %v", status, data)
	}
	servers, _ := data["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("应 1 条: %v", servers)
	}
	first, _ := servers[0].(map[string]any)
	sug, _ := first["suggestion"].(map[string]any)
	if sug == nil || sug["command"] != "npx" {
		t.Fatalf("应补 npx 建议: %+v", sug)
	}
	args, _ := sug["args"].([]any)
	if len(args) != 2 || fmt.Sprint(args[1]) != "codebase-memory-mcp" {
		t.Fatalf("npx 参数不符: %v", args)
	}
	if !strings.Contains(fmt.Sprint(first["description"]), "npm 包回退") {
		t.Fatalf("描述应附回退说明: %v", first["description"])
	}
}

// npm 无同名包（404）→ suggestion 保持 null，不出假建议
func TestRegistrySearchNoFalseSuggestion(t *testing.T) {
	h := newAPIHarness(t)
	restore := stubUpstreams(t, map[string]stubRoute{
		"/v0/servers": {body: `{
			"servers": [{"server": {
				"name": "io.github.DeusData/codebase-memory-mcp",
				"description": "Persistent codebase knowledge graph.",
				"packages": [{"registryType": "mcpb", "identifier": "https://github.com/x/y/x.zip", "transport": {"type": "stdio"}}]
			}}],
			"metadata": {"total": 1}
		}`},
		"/codebase-memory-mcp/latest": {body: `{"error": "Not Found"}`, status: 404},
	})
	defer restore()

	status, data := h.api("GET", "/api/registry/search?q=codebase-memory-mcp&limit=20&offset=0&source=official", nil)
	if status != http.StatusOK {
		t.Fatalf("应 200: %d", status)
	}
	servers, _ := data["servers"].([]any)
	first, _ := servers[0].(map[string]any)
	if first["suggestion"] != nil {
		t.Fatalf("npm 404 时建议应保持 null: %+v", first["suggestion"])
	}
}

// npm 源：相关性过滤生效，同名精确包排第一（不被高下载模糊匹配埋掉）
func TestRegistrySearchNpmRelevance(t *testing.T) {
	h := newAPIHarness(t)
	restore := stubUpstreams(t, map[string]stubRoute{
		"/-/v1/search": {body: `{
			"total": 3,
			"objects": [
				{"package": {"name": "@better-auth/memory-adapter", "description": "Memory adapter"}, "downloads": {"weekly": 11054730}},
				{"package": {"name": "codebase-memory-mcp", "description": "Fast code intelligence", "version": "0.12.1"}, "downloads": {"weekly": 8008}},
				{"package": {"name": "mongodb-memory-server", "description": "MongoDB in-memory"}, "downloads": {"weekly": 2164713}}
			]
		}`},
	})
	defer restore()

	status, data := h.api("GET", "/api/registry/search?q=codebase-memory-mcp&limit=20&offset=0&source=npm", nil)
	if status != http.StatusOK {
		t.Fatalf("应 200: %d %v", status, data)
	}
	servers, _ := data["servers"].([]any)
	if len(servers) != 1 || fmt.Sprint(servers[0].(map[string]any)["name"]) != "codebase-memory-mcp" {
		t.Fatalf("精确命中应唯一: %v", servers)
	}
}

// npm 源无关键词 → 400 明确提示
func TestRegistrySearchNpmNeedsQuery(t *testing.T) {
	h := newAPIHarness(t)
	status, data := h.api("GET", "/api/registry/search?source=npm", nil)
	if status != http.StatusBadRequest {
		t.Fatalf("npm 空词应 400: %d", status)
	}
	if !strings.Contains(fmt.Sprint(data["error"]), "npm 源需要搜索关键词") {
		t.Fatalf("提示文案不符: %v", data["error"])
	}
}
