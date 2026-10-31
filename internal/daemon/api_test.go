package daemon

// 管理 API 测试（移植自 TS packages/server/test/api.test.ts）。
// 沙箱纪律：agent 配置也必须重定向，绝不触碰真实 agent 配置。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hxgh776/beaver/internal/store"
)

type apiHarness struct {
	t       *testing.T
	d       *Daemon
	base    string
	token   string
	sandbox string
}

func newAPIHarness(t *testing.T) *apiHarness {
	t.Helper()
	sandbox := t.TempDir()
	d := New(Options{HomeDir: sandbox, AgentConfigRoot: sandbox, Port: intPtr(0)})
	if err := d.Start(); err != nil {
		t.Fatalf("daemon 启动失败: %v", err)
	}
	h := &apiHarness{t: t, d: d, base: d.BaseURL(), token: d.Config.Settings.Token, sandbox: sandbox}
	t.Cleanup(func() { _ = d.Stop() })
	return h
}

func (h *apiHarness) api(method, pathname string, body any) (int, map[string]any) {
	h.t.Helper()
	return h.apiToken(method, pathname, body, h.token)
}

func (h *apiHarness) apiToken(method, pathname string, body any, token string) (int, map[string]any) {
	h.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rdr = bytes.NewReader(data)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, h.base+pathname, rdr)
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
	_ = json.NewDecoder(res.Body).Decode(&data)
	return res.StatusCode, data
}

func (h *apiHarness) raw(pathname string) (int, map[string]any) {
	h.t.Helper()
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Get(h.base + pathname)
	if err != nil {
		h.t.Fatalf("请求失败: %v", err)
	}
	defer res.Body.Close()
	var data map[string]any
	_ = json.NewDecoder(res.Body).Decode(&data)
	return res.StatusCode, data
}

func intPtr(i int) *int { return &i }

// D7 令牌开关：默认关闭无 token 直接 200
func TestAuthDefaultOff(t *testing.T) {
	h := newAPIHarness(t)
	status, _ := h.raw("/api/status")
	if status != http.StatusOK {
		t.Fatalf("无 token 应 200，得到 %d", status)
	}
	_, info := h.raw("/api/auth/info")
	if info["authRequired"] != false {
		t.Errorf("默认应关闭鉴权")
	}
}

// D7 令牌开关：开启后无/错 token 401，query token 放行；关闭恢复
func TestAuthToggle(t *testing.T) {
	h := newAPIHarness(t)
	if status, _ := h.api("PATCH", "/api/settings", map[string]any{"authRequired": true}); status != http.StatusOK {
		t.Fatalf("PATCH 失败")
	}
	status, _ := h.raw("/api/status")
	if status != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401，得到 %d", status)
	}
	wrong, _ := h.apiToken("GET", "/api/status", nil, "wrong-token")
	if wrong != http.StatusUnauthorized {
		t.Errorf("错 token 应 401，得到 %d", wrong)
	}
	viaQuery := h.base + "/api/status?token=" + h.token
	client := &http.Client{}
	res, err := client.Get(viaQuery)
	if err != nil {
		t.Fatalf("query token 请求失败: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("query token 应放行，得到 %d", res.StatusCode)
	}
	// auth/info 仍然公开
	infoStatus, _ := h.raw("/api/auth/info")
	if infoStatus != http.StatusOK {
		t.Errorf("auth/info 应公开")
	}
	// 关闭恢复
	h.api("PATCH", "/api/settings", map[string]any{"authRequired": false})
	if status, _ = h.raw("/api/status"); status != http.StatusOK {
		t.Errorf("关闭后应恢复 200，得到 %d", status)
	}
}

// I-AP-01 注册表 CRUD 全链路
func TestRegistryCRUDViaAPI(t *testing.T) {
	h := newAPIHarness(t)
	created, data := h.api("POST", "/api/servers", map[string]any{
		"name": "Filesystem", "transport": "stdio", "command": "npx",
		"args": []string{"-y", "@modelcontextprotocol/server-filesystem"},
		"env":  map[string]any{"FS_ROOT": `D:\x`},
	})
	if created != http.StatusCreated {
		t.Fatalf("创建应 201，得到 %d: %v", created, data)
	}
	server, _ := data["server"].(map[string]any)
	if server["id"] != "filesystem" {
		t.Fatalf("slug id 应为 filesystem，得到 %v", server["id"])
	}

	_, listed := h.api("GET", "/api/servers", nil)
	if servers, _ := listed["servers"].([]any); len(servers) != 1 {
		t.Fatalf("应有 1 个 server")
	}

	if _, patched := h.api("PATCH", "/api/servers/filesystem", map[string]any{"gatewayMode": true}); patched["server"].(map[string]any)["gatewayMode"] != true {
		t.Errorf("PATCH gatewayMode 未生效")
	}

	_, toolOverride := h.api("PATCH", "/api/servers/filesystem", map[string]any{
		"toolOverrides": map[string]any{"write_file": map[string]any{"enabled": false}},
	})
	ov, _ := toolOverride["server"].(map[string]any)["toolOverrides"].(map[string]any)
	wf, _ := ov["write_file"].(map[string]any)
	if wf["enabled"] != false {
		t.Errorf("toolOverrides PATCH 未生效")
	}

	_, dup := h.api("POST", "/api/servers", map[string]any{"name": "Filesystem", "transport": "stdio", "command": "npx"})
	if dup["server"].(map[string]any)["id"] != "filesystem-2" {
		t.Errorf("重复 id 应为 filesystem-2")
	}

	if status, _ := h.api("DELETE", "/api/servers/filesystem", nil); status != http.StatusOK {
		t.Errorf("删除应 200")
	}
	_, listed = h.api("GET", "/api/servers", nil)
	if servers, _ := listed["servers"].([]any); len(servers) != 1 {
		t.Errorf("删除后应剩 1 个 server")
	}

	invalid, msg := h.api("POST", "/api/servers", map[string]any{"name": "X", "transport": "stdio"})
	if invalid != http.StatusBadRequest {
		t.Errorf("缺 command 应 400，得到 %d", invalid)
	}
	// Issue-2：校验错误为友好中文文案
	errMsg, _ := msg["error"].(string)
	if !strings.Contains(errMsg, "命令") || !strings.Contains(errMsg, "stdio") {
		t.Errorf("错误文案不符: %q", errMsg)
	}
	if strings.Contains(errMsg, "too_small") {
		t.Errorf("不应暴露 zod 原始信息: %q", errMsg)
	}
}

// I-SY over HTTP：dryRun 不落盘；真实同步写入沙箱配置
func TestSyncViaAPI(t *testing.T) {
	h := newAPIHarness(t)
	h.api("POST", "/api/servers", map[string]any{"name": "fs", "transport": "stdio", "command": "node", "args": []string{"server.js"}})
	if status, _ := h.api("PUT", "/api/bindings/cursor/fs", nil); status != http.StatusOK {
		t.Fatalf("绑定失败")
	}

	if status, _ := h.api("POST", "/api/sync", map[string]any{"dryRun": true}); status != http.StatusOK {
		t.Fatalf("dry-run 失败")
	}
	cursorFile := filepath.Join(h.sandbox, ".cursor", "mcp.json")
	if _, err := os.Stat(cursorFile); !os.IsNotExist(err) {
		t.Fatalf("dry-run 不应落盘")
	}

	if status, _ := h.api("POST", "/api/sync", map[string]any{}); status != http.StatusOK {
		t.Fatalf("同步失败")
	}
	data, err := os.ReadFile(cursorFile)
	if err != nil {
		t.Fatalf("cursor 配置未写入: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(data, &doc)
	servers, _ := doc["mcpServers"].(map[string]any)
	fs, _ := servers["fs"].(map[string]any)
	if fs["command"] != "node" {
		t.Fatalf("cursor 配置内容不符: %v", doc)
	}
}

// I-AP-02 agents 探测结构与 settings 修改/token 轮换
func TestAgentsAndSettings(t *testing.T) {
	h := newAPIHarness(t)
	_, agents := h.api("GET", "/api/agents", nil)
	list, _ := agents["agents"].([]any)
	found := false
	for _, a := range list {
		m, _ := a.(map[string]any)
		if fmt.Sprint(m["agentType"]) == "claude-code" {
			found = true
		}
	}
	if !found {
		t.Fatalf("agents 应包含 claude-code")
	}

	if status, _ := h.api("PATCH", "/api/settings", map[string]any{"logLevel": "debug"}); status != http.StatusOK {
		t.Errorf("logLevel PATCH 失败")
	}
	_, settings := h.api("GET", "/api/settings", nil)
	if settings["logLevel"] != "debug" {
		t.Errorf("logLevel 未生效")
	}

	if status, _ := h.api("PATCH", "/api/settings", map[string]any{"port": 99999}); status != http.StatusBadRequest {
		t.Errorf("非法端口应 400，得到 %d", status)
	}

	// 轮换在令牌开启时才有"待重同步"语义
	h.api("PATCH", "/api/settings", map[string]any{"authRequired": true})
	_, rotate := h.api("POST", "/api/settings/token/rotate", nil)
	newToken, _ := rotate["token"].(string)
	if newToken == "" || newToken == h.token {
		t.Fatalf("token 应轮换")
	}
	// 旧 token 立即失效
	oldAuth, _ := h.apiToken("GET", "/api/status", nil, h.token)
	if oldAuth != http.StatusUnauthorized {
		t.Errorf("旧 token 应 401，得到 %d", oldAuth)
	}
	h.token = newToken
}

// /mcp 网关未挂载时 503 GATEWAY_DOWN（与 TS daemon.handle 同序：钩子缺省即 503；
// 挂载后的 401 鉴权路径由 gateway 包集成测试覆盖）
func TestGatewayRuntimeSwitchFromDaemonSide(t *testing.T) {
	h := newAPIHarness(t)
	// 无钩子：无论鉴权开关都 503
	res, err := http.Post(h.base+"/mcp", "application/json", nil)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("网关未挂载 /mcp 应 503，得到 %d", res.StatusCode)
	}

	// 网关停止（daemon 单元测试用桩替代真实网关：清掉 HTTP 钩子 = detach）
	d := h.d
	d.GatewayControl = func(action string) (GatewayStatus, error) {
		if action == "stop" {
			d.GatewayHTTP = nil
		}
		return GatewayStatus{}, nil
	}
	if status, _ := h.api("POST", "/api/gateway/stop", nil); status != http.StatusOK {
		t.Fatalf("stop 应 200")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("POST", h.base+"/mcp", nil)
	req.Header.Set("authorization", "Bearer "+h.token)
	res, err = client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("网关停止后应 503，得到 %d", res.StatusCode)
	}
}

// M2.2 环境探测：node 必在且 found（本机无 node 则跳过）
func TestEnvironmentDetect(t *testing.T) {
	h := newAPIHarness(t)
	status, data := h.api("GET", "/api/environment", nil)
	if status != http.StatusOK {
		t.Fatalf("environment 失败")
	}
	tools, _ := data["tools"].([]any)
	hasUVX := false
	nodeFound := false
	for _, raw := range tools {
		m, _ := raw.(map[string]any)
		if fmt.Sprint(m["name"]) == "uvx" {
			hasUVX = true
		}
		if fmt.Sprint(m["name"]) == "node" {
			nodeFound, _ = m["found"].(bool)
		}
	}
	if !hasUVX {
		t.Errorf("tools 应包含 uvx")
	}
	if _, err := exec.LookPath("node"); err == nil && !nodeFound {
		t.Errorf("本机有 node 但探测为未找到")
	}
}

// M2.4 token 轮换提醒：rotate 后 resyncPending，网关重写后清除
func TestTokenRotationResyncPending(t *testing.T) {
	h := newAPIHarness(t)
	h.api("PATCH", "/api/settings", map[string]any{"authRequired": true})
	h.api("POST", "/api/servers", map[string]any{
		"name": "gw-srv", "transport": "stdio", "command": "node", "args": []string{"x.js"}, "gatewayMode": true,
	})
	h.api("PUT", "/api/bindings/cursor/gw-srv", nil)
	h.api("POST", "/api/sync", map[string]any{})
	if _, st := h.api("GET", "/api/status", nil); st["resyncPending"] != false {
		t.Fatalf("同步后 resyncPending 应为 false")
	}

	rotated, rdata := h.api("POST", "/api/settings/token/rotate", nil)
	h.token, _ = rdata["token"].(string) // 后续请求必须用新 token
	if rotated != http.StatusOK {
		t.Fatalf("rotate 失败")
	}
	if _, st := h.api("GET", "/api/status", nil); st["resyncPending"] != true {
		t.Fatalf("轮换后 resyncPending 应为 true")
	}

	// 重新同步：网关条目带新 token 重写 → 提醒消除
	h.api("POST", "/api/sync", map[string]any{})
	if _, st := h.api("GET", "/api/status", nil); st["resyncPending"] != false {
		t.Fatalf("重同步后 resyncPending 应为 false")
	}
	h.api("PATCH", "/api/settings", map[string]any{"authRequired": false})
}

// 配置变更被持久化，可被下一个 Store 实例读到
func TestStoreRoundTripThroughDaemon(t *testing.T) {
	h := newAPIHarness(t)
	if status, _ := h.api("POST", "/api/servers", map[string]any{"name": "persist", "transport": "stdio", "command": "node"}); status != http.StatusCreated {
		t.Fatalf("创建失败")
	}
	s := store.NewStore(h.sandbox)
	config, err := s.Load()
	if err != nil {
		t.Fatalf("重读配置失败: %v", err)
	}
	found := false
	for _, srv := range config.Servers {
		if srv.ID == "persist" {
			found = true
		}
	}
	if !found {
		t.Fatalf("配置未持久化 persist")
	}
}
