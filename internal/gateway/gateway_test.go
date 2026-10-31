package gateway

// 网关集成测试（I-GW）：真实 stdio/http 上游 ↔ daemon ↔ go-sdk 客户端。
// 移植自 TS packages/server/test/gateway.test.ts；stdio 夹具用测试二进制自举。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hxgh776/beaver/internal/daemon"
)

// fakeStdioCmd 返回运行 fake stdio server 的命令（本测试二进制重入；不带 -test.v 以免污染 stdout 协议通道）。
func fakeStdioCmd(t *testing.T, magic string) (string, []string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("解析测试二进制失败: %v", err)
	}
	return exe, []string{"-test.run=TestHelperFakeStdio"}
}

type testHarness struct {
	t       *testing.T
	d       *daemon.Daemon
	base    string
	token   string
	sandbox string
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()
	sandbox := t.TempDir()
	d := daemon.New(daemon.Options{HomeDir: sandbox, AgentConfigRoot: sandbox, Port: intPtr(0)})
	if err := d.Start(); err != nil {
		t.Fatalf("daemon 启动失败: %v", err)
	}
	gw := New(d)
	gw.Attach()
	h := &testHarness{t: t, d: d, base: d.BaseURL(), token: d.Config.Settings.Token, sandbox: sandbox}
	t.Cleanup(func() {
		_ = gw.Detach()
		_ = d.Stop()
	})
	return h
}

func intPtr(i int) *int { return &i }

func strp(s string) *string { return &s }

func (h *testHarness) api(method, pathname string, body any) (int, map[string]any) {
	h.t.Helper()
	return h.apiToken(method, pathname, body, h.token)
}

func (h *testHarness) apiToken(method, pathname string, body any, token string) (int, map[string]any) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+pathname, marshalBody(body))
	if err != nil {
		h.t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		h.t.Fatalf("请求失败: %v", err)
	}
	defer res.Body.Close()
	var data map[string]any
	_ = jsonDecode(res.Body, &data)
	return res.StatusCode, data
}

// addStdioUpstream 注册一个 stdio 上游（BEAVER_FAKE_STDIO=1 触发自举夹具）。
func (h *testHarness) addStdioUpstream(id string) {
	h.t.Helper()
	exe, args := fakeStdioCmd(h.t, "")
	env := map[string]string{"MAGIC_VALUE": "magic-" + id, "BEAVER_FAKE_STDIO": "1"}
	status, data := h.api("POST", "/api/servers", map[string]any{
		"id": id, "name": id, "transport": "stdio",
		"command": exe, "args": args, "env": env, "gatewayMode": true,
	})
	if status != http.StatusCreated {
		h.t.Fatalf("注册 %s 失败: %v", id, data)
	}
}

// addStdioUpstreams 注册两个 stdio 上游（工具重名用于前缀测试）。
func (h *testHarness) addStdioUpstreams() {
	h.t.Helper()
	h.addStdioUpstream("fs-a")
	h.addStdioUpstream("fs-b")
}

// connectClient 连接网关聚合端点（path 为 /mcp、/servers/:id/mcp 或 /agents/:type/mcp）。
func (h *testHarness) connectClient(pathname string) *mcp.ClientSession {
	h.t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "gateway-test", Version: "1.0.0"}, nil)
	endpoint := h.base + pathname
	tr := &headerRoundTripper{token: h.token}
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: tr, Timeout: 60 * time.Second},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		h.t.Fatalf("连接网关失败: %v", err)
	}
	return sess
}

type headerRoundTripper struct{ token string }

func (t *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(req)
}

func toolNames(result *mcp.ListToolsResult) []string {
	names := make([]string, 0, len(result.Tools))
	for _, t := range result.Tools {
		names = append(names, t.Name)
	}
	return names
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// I-GW-01 聚合 listTools + callTool + env 凭证透传
func TestGatewayAggregateListAndCall(t *testing.T) {
	h := newHarness(t)
	h.addStdioUpstreams()
	up := startFakeHTTPUpstream(t)
	if status, _ := h.api("POST", "/api/servers", map[string]any{
		"id": "web", "name": "web", "transport": "http",
		"url": *up.url, "gatewayMode": true,
	}); status != http.StatusCreated {
		t.Fatalf("注册 http 上游失败")
	}
	if status, _ := h.api("POST", "/api/gateway/start", nil); status != http.StatusOK {
		t.Fatalf("网关启动失败")
	}

	client := h.connectClient("/mcp")
	defer client.Close()
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("listTools 失败: %v", err)
	}
	// fs-a / fs-b 工具重名 → 前缀；web 的 http_tool 唯一 → 透传
	want := []string{"fs-a__add", "fs-a__echo", "fs-a__env", "fs-b__add", "fs-b__echo", "fs-b__env", "http_tool"}
	got := toolNames(tools)
	if len(got) != len(want) {
		t.Fatalf("工具列表不符: got %v want %v", got, want)
	}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("缺少工具 %s: %v", w, got)
		}
	}

	echo, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "fs-a__echo", Arguments: map[string]any{"message": "hi"}})
	if err != nil {
		t.Fatalf("call echo 失败: %v", err)
	}
	if textOf(echo) != "echo: hi" {
		t.Errorf("echo 返回不符: %q", textOf(echo))
	}

	add, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "fs-b__add", Arguments: map[string]any{"a": 2, "b": 3}})
	if err != nil {
		t.Fatalf("call add 失败: %v", err)
	}
	if textOf(add) != "5" {
		t.Errorf("add 返回不符: %q", textOf(add))
	}

	env, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "fs-a__env", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call env 失败: %v", err)
	}
	if textOf(env) != "magic-fs-a" {
		t.Errorf("env 凭证透传不符: %q", textOf(env))
	}

	httpTool, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "http_tool", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call http_tool 失败: %v", err)
	}
	if textOf(httpTool) != "http:http_tool" {
		t.Errorf("http_tool 返回不符: %q", textOf(httpTool))
	}
}

// I-GW-03 崩溃自愈：杀上游进程后再次调用自动恢复
func TestGatewayCrashRecovery(t *testing.T) {
	h := newHarness(t)
	h.addStdioUpstreams()
	if status, _ := h.api("POST", "/api/gateway/start", nil); status != http.StatusOK {
		t.Fatalf("网关启动失败")
	}
	client := h.connectClient("/mcp")
	defer client.Close()
	if _, err := client.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("listTools 失败: %v", err)
	}
	if status, _ := h.api("POST", "/api/servers/fs-a/start", nil); status != http.StatusOK {
		t.Fatalf("显式拉起失败")
	}
	_, data := h.api("GET", "/api/gateway", nil)
	var pid float64
	for _, u := range data["upstreams"].([]any) {
		m, _ := u.(map[string]any)
		if fmt.Sprint(m["id"]) == "fs-a" {
			pid, _ = m["pid"].(float64)
		}
	}
	if pid <= 0 {
		t.Fatalf("fs-a 应有 pid")
	}
	killProcess(int(pid))
	time.Sleep(500 * time.Millisecond)

	// 再次调用：应自动重启上游并成功
	res, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "fs-a__echo", Arguments: map[string]any{"message": "back"}})
	if err != nil {
		t.Fatalf("崩溃自愈后调用失败: %v", err)
	}
	if textOf(res) != "echo: back" {
		t.Errorf("自愈后返回不符: %q", textOf(res))
	}
}

// I-GW-04 上游错误与未知工具传播
func TestGatewayUnknownTool(t *testing.T) {
	h := newHarness(t)
	h.addStdioUpstreams()
	if status, _ := h.api("POST", "/api/gateway/start", nil); status != http.StatusOK {
		t.Fatalf("网关启动失败")
	}
	client := h.connectClient("/mcp")
	defer client.Close()
	_, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "no_such_tool", Arguments: map[string]any{}})
	if err == nil {
		t.Fatalf("未知工具应报错")
	}
	if !contains([]string{err.Error()}, "") && !stringsContains(err.Error(), "未知工具") {
		t.Errorf("错误信息应含「未知工具」: %v", err)
	}
}

// 上游进程控制 API：stop 后状态 idle
func TestGatewayUpstreamStopIdle(t *testing.T) {
	h := newHarness(t)
	h.addStdioUpstreams()
	if status, _ := h.api("POST", "/api/gateway/start", nil); status != http.StatusOK {
		t.Fatalf("网关启动失败")
	}
	if status, _ := h.api("POST", "/api/servers/fs-a/start", nil); status != http.StatusOK {
		t.Fatalf("启动失败")
	}
	if status, _ := h.api("POST", "/api/servers/fs-a/stop", nil); status != http.StatusOK {
		t.Fatalf("stop 失败")
	}
	_, data := h.api("GET", "/api/gateway", nil)
	for _, u := range data["upstreams"].([]any) {
		m, _ := u.(map[string]any)
		if fmt.Sprint(m["id"]) == "fs-a" && m["status"] != "idle" {
			t.Fatalf("stop 后 fs-a 应为 idle，得到 %v", m["status"])
		}
	}
}

// I-GW-02 懒启动：未使用时快照列出 idle 占位而非缺席
func TestGatewayLazyStartIdleSnapshots(t *testing.T) {
	h := newHarness(t)
	h.addStdioUpstreams()
	if status, _ := h.api("POST", "/api/gateway/start", nil); status != http.StatusOK {
		t.Fatalf("网关启动失败")
	}
	status, data := h.api("GET", "/api/gateway", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/gateway 失败")
	}
	if running, _ := data["running"].(bool); !running {
		t.Fatalf("网关应为 running")
	}
	upstreams, _ := data["upstreams"].([]any)
	ids := map[string]map[string]any{}
	for _, u := range upstreams {
		m, _ := u.(map[string]any)
		ids[fmt.Sprint(m["id"])] = m
	}
	for _, id := range []string{"fs-a", "fs-b"} {
		m, ok := ids[id]
		if !ok {
			t.Fatalf("上游 %s 未出现在快照", id)
		}
		if m["status"] != "idle" {
			t.Errorf("上游 %s 应为 idle，得到 %v", id, m["status"])
		}
		if m["pid"] != nil {
			t.Errorf("idle 上游不应有 pid")
		}
		if tc, _ := m["toolCount"].(float64); tc != 0 {
			t.Errorf("idle 上游 toolCount 应为 0")
		}
	}
}

// 上游启动失败：快照标记 error 并保留错误信息
func TestGatewayUpstreamStartFailure(t *testing.T) {
	h := newHarness(t)
	boomCmd := "nonexistent-beaver-cmd-xyz"
	h.api("POST", "/api/servers", map[string]any{
		"id": "boom", "name": "boom", "transport": "stdio", "command": boomCmd, "gatewayMode": true,
	})
	if status, _ := h.api("POST", "/api/servers/boom/start", nil); status != http.StatusInternalServerError {
		t.Fatalf("启动失败应回传 500")
	}
	_, data := h.api("GET", "/api/gateway", nil)
	upstreams, _ := data["upstreams"].([]any)
	for _, u := range upstreams {
		m, _ := u.(map[string]any)
		if fmt.Sprint(m["id"]) == "boom" {
			if m["status"] != "error" {
				t.Errorf("boom 应为 error，得到 %v", m["status"])
			}
			if s, _ := m["lastError"].(string); s == "" {
				t.Errorf("boom 应有 lastError")
			}
			return
		}
	}
	t.Fatalf("boom 未出现在快照")
}

// I-GW-05 工具停用：从列表剔除，调用返回停用错误 (G4)
func TestGatewayToolDisable(t *testing.T) {
	h := newHarness(t)
	h.addStdioUpstreams()
	if status, _ := h.api("POST", "/api/gateway/start", nil); status != http.StatusOK {
		t.Fatalf("网关启动失败")
	}
	if status, _ := h.api("PATCH", "/api/servers/fs-a", map[string]any{
		"toolOverrides": map[string]any{"echo": map[string]any{"enabled": false}},
	}); status != http.StatusOK {
		t.Fatalf("PATCH 失败")
	}
	client := h.connectClient("/mcp")
	defer client.Close()
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("listTools 失败: %v", err)
	}
	names := toolNames(tools)
	if contains(names, "fs-a__echo") {
		t.Errorf("fs-a__echo 应被剔除: %v", names)
	}
	// fs-a 的 echo 停用后不再冲突 → fs-b 的 echo 回退为无前缀名
	if !contains(names, "echo") {
		t.Errorf("echo 应回退为无前缀名: %v", names)
	}
	disabled, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "fs-a__echo", Arguments: map[string]any{"message": "x"}})
	if err != nil {
		t.Fatalf("调用停用工具不应传输层报错: %v", err)
	}
	if !disabled.IsError {
		t.Errorf("停用工具应 isError")
	}
	if text := textOf(disabled); !contains([]string{text}, "停用") == false && text == "" {
		t.Errorf("停用文案不符: %q", text)
	}
}

// I-GW-06 per-server 独立端点：原始工具名（G6）
func TestGatewayPerServerEndpoint(t *testing.T) {
	h := newHarness(t)
	h.addStdioUpstreams()
	if status, _ := h.api("POST", "/api/gateway/start", nil); status != http.StatusOK {
		t.Fatalf("网关启动失败")
	}
	client := h.connectClient("/servers/fs-a/mcp")
	defer client.Close()
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("listTools 失败: %v", err)
	}
	// 不带前缀
	if len(tools.Tools) != 3 {
		t.Fatalf("per-server 工具应为原始 3 个，得到 %v", toolNames(tools))
	}
	res, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"message": "single"}})
	if err != nil {
		t.Fatalf("callTool 失败: %v", err)
	}
	if textOf(res) != "echo: single" {
		t.Errorf("返回不符: %q", textOf(res))
	}
}

// D7 令牌开关：默认关闭时无凭证可访问；开启后无 token 401
func TestGatewayTokenSwitch(t *testing.T) {
	h := newHarness(t)
	h.addStdioUpstreams()
	if status, _ := h.api("POST", "/api/gateway/start", nil); status != http.StatusOK {
		t.Fatalf("网关启动失败")
	}
	// 默认关闭：无 token 客户端可用
	client := mcp.NewClient(&mcp.Implementation{Name: "no-auth-client", Version: "1.0.0"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: h.base + "/mcp", DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("无凭证连接失败: %v", err)
	}
	if _, err := sess.ListTools(context.Background(), nil); err != nil {
		t.Errorf("无凭证 listTools 失败: %v", err)
	}
	_ = sess.Close()

	// 开启后：无 token 401
	h.api("PATCH", "/api/settings", map[string]any{"authRequired": true})
	res, err := http.Post(h.base+"/mcp", "application/json", nil)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("开启鉴权后无 token 应 401，得到 %d", res.StatusCode)
	}
	h.api("PATCH", "/api/settings", map[string]any{"authRequired": false})
}

// I-GW-08 调用日志记录 server/tool/耗时 (G11) + Issue-3 跨重启持久
func TestGatewayCallLogsPersistAcrossRestart(t *testing.T) {
	h := newHarness(t)
	h.addStdioUpstreams()
	if status, _ := h.api("POST", "/api/gateway/start", nil); status != http.StatusOK {
		t.Fatalf("网关启动失败")
	}
	client := h.connectClient("/mcp")
	if _, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "fs-a__echo", Arguments: map[string]any{"message": "log"}}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	_ = client.Close()
	_, data := h.api("GET", "/api/logs/calls", nil)
	calls, _ := data["calls"].([]any)
	found := false
	for _, c := range calls {
		m, _ := c.(map[string]any)
		if fmt.Sprint(m["tool"]) == "echo" && fmt.Sprint(m["serverId"]) == "fs-a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("调用日志缺失 echo@fs-a: %v", calls)
	}

	// 重启 daemon（同一数据目录）
	homeDir := h.sandbox
	gw := newGatewayFor(h)
	_ = gw.Detach()
	_ = h.d.Stop()
	d2 := daemon.New(daemon.Options{HomeDir: homeDir, AgentConfigRoot: homeDir, Port: intPtr(0)})
	if err := d2.Start(); err != nil {
		t.Fatalf("重启失败: %v", err)
	}
	gw2 := New(d2)
	gw2.Attach()
	t.Cleanup(func() {
		_ = gw2.Detach()
		_ = d2.Stop()
	})
	h.d = d2
	h.base = d2.BaseURL() // 临时端口每次重启都变
	h.token = d2.Config.Settings.Token
	_, data = h.api("GET", "/api/logs/calls", nil)
	calls, _ = data["calls"].([]any)
	found = false
	for _, c := range calls {
		m, _ := c.(map[string]any)
		if fmt.Sprint(m["tool"]) == "echo" && fmt.Sprint(m["serverId"]) == "fs-a" && fmt.Sprint(m["scope"]) != "debug" {
			found = true
		}
	}
	if !found {
		t.Fatalf("重启后调用日志丢失")
	}
}

func newGatewayFor(h *testHarness) *Gateway {
	return New(h.d)
}

// M3.3 per-agent 分组端点：agent 只看到自己绑定的网关 server
func TestGatewayPerAgentEndpoint(t *testing.T) {
	h := newHarness(t)
	h.addStdioUpstreams()
	if status, _ := h.api("POST", "/api/gateway/start", nil); status != http.StatusOK {
		t.Fatalf("网关启动失败")
	}
	if status, _ := h.api("PUT", "/api/bindings/cursor/fs-a", nil); status != http.StatusOK {
		t.Fatalf("绑定失败")
	}
	cursorClient := h.connectClient("/agents/cursor/mcp")
	tools, err := cursorClient.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("listTools 失败: %v", err)
	}
	// 作用域内只有 fs-a → 无冲突 → 无前缀
	if len(tools.Tools) != 3 {
		t.Fatalf("cursor 端点应有 3 个原始名工具，得到 %v", toolNames(tools))
	}
	res, err := cursorClient.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"message": "scoped"}})
	if err != nil {
		t.Fatalf("callTool 失败: %v", err)
	}
	if textOf(res) != "echo: scoped" {
		t.Errorf("返回不符: %q", textOf(res))
	}
	_ = cursorClient.Close()

	// claude-code 无绑定 → 空工具列表
	emptyClient := h.connectClient("/agents/claude-code/mcp")
	emptyTools, err := emptyClient.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("listTools 失败: %v", err)
	}
	if len(emptyTools.Tools) != 0 {
		t.Errorf("无绑定 agent 应得到空列表，得到 %v", toolNames(emptyTools))
	}
	_ = emptyClient.Close()
}

// M3.1 调试台：不经 agent 直接调用工具（S6）
func TestGatewayDebugInvoke(t *testing.T) {
	h := newHarness(t)
	h.addStdioUpstreams()
	if status, _ := h.api("POST", "/api/gateway/start", nil); status != http.StatusOK {
		t.Fatalf("网关启动失败")
	}
	status, data := h.api("POST", "/api/servers/fs-a/invoke", map[string]any{
		"tool": "echo", "arguments": map[string]any{"message": "from-debug-console"},
	})
	if status != http.StatusOK {
		t.Fatalf("invoke 失败: %v", data)
	}
	result, _ := data["result"].(map[string]any)
	content, _ := result["content"].([]any)
	first, _ := content[0].(map[string]any)
	if fmt.Sprint(first["text"]) != "echo: from-debug-console" {
		t.Errorf("调试台返回不符: %v", first["text"])
	}
	// 调用也进入日志（scope=debug）
	_, data = h.api("GET", "/api/logs/calls", nil)
	calls, _ := data["calls"].([]any)
	okScope := false
	for _, c := range calls {
		m, _ := c.(map[string]any)
		if fmt.Sprint(m["scope"]) == "debug" && fmt.Sprint(m["tool"]) == "echo" {
			okScope = true
		}
	}
	if !okScope {
		t.Errorf("调试台调用未入日志")
	}

	// 不存在的工具 → 502
	if status, _ = h.api("POST", "/api/servers/fs-a/invoke", map[string]any{"tool": "nope", "arguments": map[string]any{}}); status != http.StatusBadGateway {
		t.Errorf("未知工具应 502，得到 %d", status)
	}
}

// —— fake HTTP 上游（真实 streamable HTTP MCP server） —— //

type fakeUpstream struct {
	url *string
}

func startFakeHTTPUpstream(t *testing.T) fakeUpstream {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-http-upstream", Version: "1.0.0"},
		&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}})
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				return &mcp.ListToolsResult{Tools: []*mcp.Tool{{
					Name: "http_tool", Description: "from http upstream",
					InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
				}}}, nil
			case "tools/call":
				params := req.(*mcp.ServerRequest[*mcp.CallToolParamsRaw]).Params
				return &mcp.CallToolResult{Content: []mcp.Content{
					&mcp.TextContent{Text: "http:" + params.Name},
				}}, nil
			}
			return next(ctx, method, req)
		}
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return fakeUpstream{url: strp(ts.URL + "/mcp")}
}

func textOf(r *mcp.CallToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	if tc, ok := r.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return fmt.Sprint(r.Content[0])
}

func stringsContains(s, substr string) bool {
	return strings.Contains(s, substr)
}
