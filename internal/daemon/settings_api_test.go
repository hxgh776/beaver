package daemon

// 设置页配套路由测试：备份列表/恢复（清 syncState）、hasOnboarded、config 滚动备份。
// 自启 PUT 会写真实注册表/系统——只在单测里验证 GET 形状，不测 PUT。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// GET /api/autostart 形状（不触碰真实系统状态）
func TestAutostartGetShape(t *testing.T) {
	h := newAPIHarness(t)
	status, data := h.api("GET", "/api/autostart", nil)
	if status != 200 {
		t.Fatalf("应 200: %d", status)
	}
	if _, ok := data["installed"].(bool); !ok {
		t.Fatalf("应含 installed 布尔: %v", data)
	}
}

// 备份列表 + 恢复：回写 agent 当前配置 + 清 syncState 哈希（防回滚被误判外部修改）
func TestBackupsListAndRestore(t *testing.T) {
	h := newAPIHarness(t)
	// 先真实同步一次产生配置 + syncState
	h.api("POST", "/api/servers", map[string]any{"name": "fs", "transport": "stdio", "command": "node"})
	h.api("PUT", "/api/bindings/claude-code/fs", nil)
	if status, _ := h.api("POST", "/api/sync", map[string]any{}); status != 200 {
		t.Fatalf("同步失败")
	}
	if _, ok := h.d.Config.SyncState["claude-code"]; !ok {
		t.Fatalf("同步后应有 claude-code syncState")
	}
	// 造一份滚动备份（同步引擎已留真实备份；这里再补一份确定内容的）
	agentFile, err := h.d.agentTargetFile("claude-code")
	if err != nil {
		t.Fatalf("选址失败: %v", err)
	}
	backupDir := filepath.Join(h.sandbox, "backups", "claude-code")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	restored := `{"mcpServers":{"fs":{"command":"node","args":["server.js"]}},"rolled-back":true}`
	if err := os.WriteFile(filepath.Join(backupDir, "claude-code-restore-me.json"), []byte(restored), 0o644); err != nil {
		t.Fatal(err)
	}

	// 列表包含该备份 + config 备份组
	status, data := h.api("GET", "/api/backups", nil)
	if status != 200 {
		t.Fatalf("backups 应 200: %d", status)
	}
	groups, _ := data["groups"].([]any)
	found := false
	for _, g := range groups {
		m, _ := g.(map[string]any)
		if fmt.Sprint(m["agentType"]) == "claude-code" {
			files, _ := m["files"].([]any)
			if len(files) > 0 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("备份列表应含 claude-code 组且有文件")
	}

	// 恢复：内容回写 + syncState 清除
	if status, rr := h.api("POST", "/api/backups/restore", map[string]any{
		"agentType": "claude-code", "name": "claude-code-restore-me.json",
	}); status != 200 {
		t.Fatalf("恢复失败: %d %v", status, rr)
	}
	after, err := os.ReadFile(agentFile)
	if err != nil {
		t.Fatalf("恢复后读配置失败: %v", err)
	}
	if !contains(string(after), `"rolled-back":true`) {
		t.Fatalf("恢复内容未落盘: %s", after)
	}
	if _, ok := h.d.Config.SyncState["claude-code"]; ok {
		t.Fatalf("恢复后应清除 syncState 哈希")
	}
	// 持久化确认
	cfg := store2Load(t, h.sandbox)
	if _, ok := cfg["syncState"].(map[string]any)["claude-code"]; ok {
		t.Fatalf("syncState 清除应持久化")
	}
}

// 非法文件名拒收
func TestBackupsRestoreRejectsTraversal(t *testing.T) {
	h := newAPIHarness(t)
	if status, _ := h.api("POST", "/api/backups/restore", map[string]any{
		"agentType": "claude-code", "name": "../../evil.json",
	}); status != 400 {
		t.Fatalf("穿越名应 400: %d", status)
	}
}

// hasOnboarded PATCH 往返 + 持久化
func TestHasOnboardedSetting(t *testing.T) {
	h := newAPIHarness(t)
	if status, _ := h.api("PATCH", "/api/settings", map[string]any{"hasOnboarded": true}); status != 200 {
		t.Fatalf("PATCH 失败")
	}
	_, data := h.api("GET", "/api/settings", nil)
	if data["hasOnboarded"] != true {
		t.Fatalf("hasOnboarded 未生效: %v", data)
	}
	cfg := store2Load(t, h.sandbox)
	if cfg["settings"].(map[string]any)["hasOnboarded"] != true {
		t.Fatalf("hasOnboarded 应持久化")
	}
}

// config.json 写前滚动备份（keep=10）
func TestConfigRollingBackup(t *testing.T) {
	h := newAPIHarness(t)
	h.api("POST", "/api/servers", map[string]any{"name": "a", "transport": "stdio", "command": "node"})
	h.api("POST", "/api/servers", map[string]any{"name": "b", "transport": "stdio", "command": "node"})
	dir := h.d.Store.ConfigBackupsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("应有 config 备份目录: %v", err)
	}
	if len(entries) < 1 {
		t.Fatalf("至少留一份 config 备份")
	}
}

// /api/daemon/stop 存在且返回 ok（不真的在测试里退出进程——仅远端模式托盘用）
func TestDaemonStopRouteShape(t *testing.T) {
	h := newAPIHarness(t)
	status, data := h.api("POST", "/api/daemon/stop", nil)
	if status != 200 || data["ok"] != true {
		t.Fatalf("stop 路由形状不符: %d %v", status, data)
	}
}

func store2Load(t *testing.T, home string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && stringsIdx(s, sub) >= 0
}

func stringsIdx(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
