package daemon

// Multica agent 集成测试（VENU-15 重构后）：multica 是一个普通 agent——
//   - 状态/连接：GET /api/agents/multica/state + PUT /api/multica/settings
//   - MCP 分发：/api/bindings + /api/sync → workspace mcp add/update/remove（含收敛删除）
//   - Skill 分发：/api/skills/:id/toggle → skill create/update/delete（SkillLibrarySink）
//
// daemon 的 multica 调用指向本测试二进制自举的 fake CLI（internal/multica/multicafake，
// 有状态模式断言库的真实增删）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxgh776/beaver/internal/multica/multicafake"
	"github.com/hxgh776/beaver/internal/store"
)

func TestMain(m *testing.M) {
	if os.Getenv(multicafake.Env) == "1" {
		os.Exit(multicafake.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// setFakeMultica 把 daemon 的 multica 指向测试二进制（fake CLI），打开调用日志并启用有状态库。
func setFakeMultica(t *testing.T, h *apiHarness) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("解析测试二进制失败: %v", err)
	}
	logPath := filepath.Join(h.sandbox, "fakecli.log")
	statePath := filepath.Join(h.sandbox, "fakestate.json")
	t.Setenv(multicafake.Env, "1")
	t.Setenv(multicafake.EnvLog, logPath)
	t.Setenv(multicafake.EnvState, statePath)
	ms := h.d.Config.Settings.Multica
	if ms == nil {
		ms = &store.MulticaSettings{}
		h.d.Config.Settings.Multica = ms
	}
	ms.Binary = exe
	ms.Profile = ""
	ms.ServerURL = ""
	ms.WorkspaceID = "ws-1"
	if err := h.d.SaveConfig(); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	return logPath
}

func setFakeMulticaPlain(t *testing.T, h *apiHarness) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("解析测试二进制失败: %v", err)
	}
	logPath := filepath.Join(h.sandbox, "fakecli.log")
	t.Setenv(multicafake.Env, "1")
	t.Setenv(multicafake.EnvLog, logPath)
	ms := h.d.Config.Settings.Multica
	if ms == nil {
		ms = &store.MulticaSettings{}
		h.d.Config.Settings.Multica = ms
	}
	ms.Binary = exe
	if err := h.d.SaveConfig(); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	return logPath
}

func fakeLogLines(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 fake 日志: %v", err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			out = append(out, strings.Split(line, multicafake.ArgvSuffix))
		}
	}
	return out
}

func containsToken(tokens []string, want string) bool {
	for _, tok := range tokens {
		if tok == want {
			return true
		}
	}
	return false
}

func countCalls(lines [][]string, match func(tokens []string) bool) int {
	n := 0
	for _, tokens := range lines {
		if match(tokens) {
			n++
		}
	}
	return n
}

func readFakeState(t *testing.T, h *apiHarness) *struct {
	Servers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"servers"`
	Skills []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"skills"`
} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.sandbox, "fakestate.json"))
	if err != nil {
		t.Fatalf("读 fake state: %v", err)
	}
	st := &struct {
		Servers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"servers"`
		Skills []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"skills"`
	}{}
	if err := json.Unmarshal(data, st); err != nil {
		t.Fatalf("解析 fake state: %v", err)
	}
	return st
}

// —— 状态读回 —— //

func TestMulticaAgentStateOK(t *testing.T) {
	h := newAPIHarness(t)
	logPath := setFakeMulticaPlain(t, h)

	status, data := h.api("GET", "/api/agents/multica/state", nil)
	if status != 200 {
		t.Fatalf("status=%d data=%v", status, data)
	}
	if data["ok"] != true {
		t.Fatalf("期望 ok=true: %v", data)
	}
	auth, ok := data["auth"].(map[string]any)
	if !ok || auth["loggedIn"] != true {
		t.Fatalf("auth 解析错误: %v", data["auth"])
	}
	if auth["user"] != "tester (tester@example.com)" {
		t.Fatalf("user 解析错误: %v", auth["user"])
	}
	workspaces, ok := data["workspaces"].([]any)
	if !ok || len(workspaces) != 2 {
		t.Fatalf("workspaces 解析错误: %v", data["workspaces"])
	}
	// 库读回分区（canned 默认）
	mcp, ok := data["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp 分区缺失: %v", data)
	}
	if servers, ok := mcp["servers"].([]any); !ok || len(servers) != 2 {
		t.Fatalf("mcp.servers 解析错误: %v", mcp)
	}
	skills, ok := data["skills"].(map[string]any)
	if !ok {
		t.Fatalf("skills 分区缺失: %v", data)
	}
	if list, ok := skills["skills"].([]any); !ok || len(list) != 1 {
		t.Fatalf("skills.skills 解析错误: %v", skills)
	}

	// 调用日志：workspace list 不应带 --workspace-id（要看全部候选）
	for _, tokens := range fakeLogLines(t, logPath) {
		if containsToken(tokens, "workspace") && containsToken(tokens, "list") {
			if containsToken(tokens, "--workspace-id") {
				t.Fatalf("workspace list 不应带 --workspace-id: %v", tokens)
			}
		}
	}
}

func TestMulticaAgentStateCLIMissing(t *testing.T) {
	h := newAPIHarness(t)
	if h.d.Config.Settings.Multica == nil {
		h.d.Config.Settings.Multica = &store.MulticaSettings{}
	}
	h.d.Config.Settings.Multica.Binary = ""
	t.Setenv("PATH", h.sandbox) // 确定性找不到 CLI
	status, data := h.api("GET", "/api/agents/multica/state", nil)
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
	if data["ok"] != false {
		t.Fatalf("期望 ok=false: %v", data)
	}
	errObj, ok := data["error"].(map[string]any)
	if !ok || errObj["code"] != "CLI_MISSING" {
		t.Fatalf("期望 CLI_MISSING: %v", data["error"])
	}
}

func TestMulticaAgentStateNotLoggedIn(t *testing.T) {
	h := newAPIHarness(t)
	setFakeMulticaPlain(t, h)
	t.Setenv(multicafake.EnvNoAuth, "1")
	status, data := h.api("GET", "/api/agents/multica/state", nil)
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
	errObj, ok := data["error"].(map[string]any)
	if !ok || errObj["code"] != "NOT_LOGGED_IN" {
		t.Fatalf("期望 NOT_LOGGED_IN: %v", data["error"])
	}
	// 未登录不应读回库（mcp 分区保持初始空表，无 error 记号）
	mcp := data["mcp"].(map[string]any)
	if _, has := mcp["error"]; has {
		t.Fatalf("未登录不应发起库读回: %v", data)
	}
}

func TestMulticaAgentStatePermissionFail(t *testing.T) {
	h := newAPIHarness(t)
	setFakeMulticaPlain(t, h)
	t.Setenv(multicafake.EnvFail, "Admin or owner role required")
	status, data := h.api("GET", "/api/agents/multica/state", nil)
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
	errObj, ok := data["error"].(map[string]any)
	if !ok || errObj["code"] != "PERMISSION_DENIED" {
		t.Fatalf("期望 PERMISSION_DENIED: %v", data["error"])
	}
}

// —— 连接设置 —— //

func TestMulticaSettingsPersistAndValidate(t *testing.T) {
	h := newAPIHarness(t)
	setFakeMulticaPlain(t, h)

	status, data := h.api("PUT", "/api/multica/settings", map[string]any{"workspaceId": "ws-2"})
	if status != 200 || data["ok"] != true {
		t.Fatalf("保存失败: status=%d data=%v", status, data)
	}
	if got := h.d.Config.Settings.Multica.WorkspaceID; got != "ws-2" {
		t.Fatalf("workspaceId 未落盘: %s", got)
	}
	// 非法 workspace 拒绝
	status, data = h.api("PUT", "/api/multica/settings", map[string]any{"workspaceId": "nope"})
	if status != 400 {
		t.Fatalf("期望 400: status=%d data=%v", status, data)
	}
	// CLI 缺失时保存路径报 503（可诊断）
	h.d.Config.Settings.Multica.Binary = ""
	t.Setenv("PATH", h.sandbox)
	status, _ = h.api("PUT", "/api/multica/settings", map[string]any{"workspaceId": "ws-1"})
	if status != 503 {
		t.Fatalf("CLI 缺失保存应 503: status=%d", status)
	}
}

// —— agent 列表 —— //

func TestMulticaInAgentsList(t *testing.T) {
	h := newAPIHarness(t)
	setFakeMultica(t, h)

	status, data := h.api("GET", "/api/agents", nil)
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
	agents, _ := data["agents"].([]any)
	var found map[string]any
	for _, a := range agents {
		if m, ok := a.(map[string]any); ok && m["agentType"] == "multica" {
			found = m
			break
		}
	}
	if found == nil {
		t.Fatalf("agent 列表缺少 multica: %v", data)
	}
	if found["detected"] != true {
		t.Fatalf("multica 应探测为已安装（fake CLI 可解析）: %v", found)
	}
	paths, _ := found["configPaths"].([]any)
	if len(paths) != 1 || paths[0] != "multica://workspace" {
		t.Fatalf("configPaths 应为虚拟路径: %v", found["configPaths"])
	}
	types, _ := data["types"].([]any)
	okType := false
	for _, tp := range types {
		if tp == "multica" {
			okType = true
		}
	}
	if !okType {
		t.Fatalf("types 应含 multica: %v", types)
	}
}

// —— MCP 分发（绑定 → 同步 → 库收敛） —— //

func addHTTPServer(t *testing.T, h *apiHarness, name, url string) string {
	t.Helper()
	status, data := h.api("POST", "/api/servers", map[string]any{
		"name": name, "transport": "http", "url": url,
	})
	if status != 201 {
		t.Fatalf("建 server 失败: status=%d data=%v", status, data)
	}
	srv := data["server"].(map[string]any)
	return srv["id"].(string)
}

func TestMulticaSyncConvergesLibrary(t *testing.T) {
	h := newAPIHarness(t)
	logPath := setFakeMultica(t, h)

	id := addHTTPServer(t, h, "linear", "https://mcp.linear.app/sse")
	if status, _ := h.api("PUT", "/api/bindings/multica/"+id, nil); status != 200 {
		t.Fatalf("绑定失败: %d", status)
	}

	// 第一次同步：add
	status, data := h.api("POST", "/api/sync", map[string]any{})
	if status != 200 {
		t.Fatalf("同步失败: status=%d data=%v", status, data)
	}
	lines := fakeLogLines(t, logPath)
	if countCalls(lines, func(tokens []string) bool {
		return containsToken(tokens, "mcp") && containsToken(tokens, "add") && containsToken(tokens, id)
	}) != 1 {
		t.Fatalf("应有且仅有一次 workspace mcp add %s: %v", id, lines)
	}
	// 推送的是临时文件通道（凭据不进命令行）
	if countCalls(lines, func(tokens []string) bool {
		return containsToken(tokens, "--server-config-file")
	}) == 0 {
		t.Fatalf("add 应走 --server-config-file: %v", lines)
	}
	// 同步报告：multica 有 upsert 且无错误
	var mcpReport map[string]any
	for _, r := range data["report"].(map[string]any)["perAgent"].([]any) {
		m := r.(map[string]any)
		if m["agentType"] == "multica" {
			mcpReport = m
		}
	}
	if mcpReport == nil || mcpReport["error"] != nil {
		t.Fatalf("multica 同步报告异常: %v", mcpReport)
	}
	// state 记账
	st := h.d.Config.SyncState["multica"]
	if len(st.KeyStates) != 1 || st.KeyStates[0].Key != id {
		t.Fatalf("SyncState[multica] 应含该键: %v", st)
	}
	// 有状态库：条目真实存在
	fake := readFakeState(t, h)
	if len(fake.Servers) != 1 || fake.Servers[0].Name != id {
		t.Fatalf("fake 库应有且仅有该条目: %v", fake.Servers)
	}

	// 第二次同步：内容未变 → no-op，不再发 add
	if status, _ := h.api("POST", "/api/sync", map[string]any{}); status != 200 {
		t.Fatalf("二次同步失败")
	}
	lines = fakeLogLines(t, logPath)
	if countCalls(lines, func(tokens []string) bool {
		return containsToken(tokens, "mcp") && containsToken(tokens, "add")
	}) != 1 {
		t.Fatalf("内容未变不应重复 add: %v", lines)
	}
	if countCalls(lines, func(tokens []string) bool {
		return containsToken(tokens, "mcp") && containsToken(tokens, "remove")
	}) != 0 {
		t.Fatalf("无多余条目不应 remove: %v", lines)
	}
}

func TestMulticaSyncRemovesUnboundAndExtras(t *testing.T) {
	h := newAPIHarness(t)
	logPath := setFakeMultica(t, h)

	id := addHTTPServer(t, h, "linear", "https://mcp.linear.app/sse")
	if status, _ := h.api("PUT", "/api/bindings/multica/"+id, nil); status != 200 {
		t.Fatalf("绑定失败: %d", status)
	}
	if status, _ := h.api("POST", "/api/sync", map[string]any{}); status != 200 {
		t.Fatalf("首次同步失败")
	}

	// 直接在 Multica 上建一个 beaver 不管的条目（追加进 fake 有状态库）→ 属于「多出来的」
	fakeState := readFakeState(t, h)
	servers := []map[string]any{}
	for _, s := range fakeState.Servers {
		servers = append(servers, map[string]any{"id": s.ID, "name": s.Name, "transport": "http"})
	}
	servers = append(servers, map[string]any{"id": "srv-manual", "name": "manual-entry", "transport": "http"})
	extra := map[string]any{"servers": servers, "skills": fakeState.Skills}
	stateData, _ := json.Marshal(extra)
	if err := os.WriteFile(filepath.Join(h.sandbox, "fakestate.json"), stateData, 0o600); err != nil {
		t.Fatalf("写 fake state 失败: %v", err)
	}

	// 解绑 → 同步：绑定条目被移除，多余条目被收敛删除
	if status, _ := h.api("DELETE", "/api/bindings/multica/"+id, nil); status != 200 {
		t.Fatalf("解绑失败: %d", status)
	}
	status, data := h.api("POST", "/api/sync", map[string]any{})
	if status != 200 {
		t.Fatalf("同步失败: status=%d data=%v", status, data)
	}
	lines := fakeLogLines(t, logPath)
	if countCalls(lines, func(tokens []string) bool {
		return containsToken(tokens, "mcp") && containsToken(tokens, "remove")
	}) != 2 {
		t.Fatalf("应恰好移除解绑条目与多余条目各一次: %v", lines)
	}
	fake := readFakeState(t, h)
	if len(fake.Servers) != 0 {
		t.Fatalf("库应被收敛为空: %v", fake.Servers)
	}
	// 报告无错误
	for _, r := range data["report"].(map[string]any)["perAgent"].([]any) {
		m := r.(map[string]any)
		if m["agentType"] == "multica" && m["error"] != nil {
			t.Fatalf("multica 同步不应报错: %v", m)
		}
	}
}

func TestMulticaSyncFailureIsolated(t *testing.T) {
	h := newAPIHarness(t)
	logPath := setFakeMultica(t, h)
	id := addHTTPServer(t, h, "linear", "https://mcp.linear.app/sse")
	if status, _ := h.api("PUT", "/api/bindings/multica/"+id, nil); status != 200 {
		t.Fatalf("绑定失败: %d", status)
	}
	// 注入权限失败：multica 的同步失败被隔离在报告里，其他 agent 照常完成
	t.Setenv(multicafake.EnvFail, "Admin or owner role required")
	status, data := h.api("POST", "/api/sync", map[string]any{})
	if status != 200 {
		t.Fatalf("远端 agent 失败不应拖垮整个同步: status=%d data=%v", status, data)
	}
	perAgent := data["report"].(map[string]any)["perAgent"].([]any)
	found := false
	for _, r := range perAgent {
		m := r.(map[string]any)
		if m["agentType"] == "multica" {
			found = true
			if m["error"] == nil {
				t.Fatalf("multica 报告应携带 error: %v", m)
			}
		} else if m["agentType"] == "claude-code" && m["error"] != nil {
			t.Fatalf("其他 agent 不应受影响: %v", m)
		}
	}
	if !found {
		t.Fatalf("报告缺 multica: %v", perAgent)
	}
	// 失败不改本地 SyncState
	if _, has := h.d.Config.SyncState["multica"]; has {
		t.Fatalf("失败的同步不应记账 SyncState[multica]")
	}
	_ = logPath
}

// —— Skill 分发（toggle → SkillLibrarySink → workspace skill 库） —— //

func seedSkill(t *testing.T, h *apiHarness) {
	t.Helper()
	dir := filepath.Join(h.sandbox, "skills", "demo-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: demo\ndescription: 测试技能\n---\n\n# demo\n正文内容"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	h.d.Config.Skills = append(h.d.Config.Skills, store.InstalledSkill{
		ID: "skill-demo", Directory: "demo-skill", Name: "demo",
		Source: "local", ContentHash: "seed", InstalledAt: "now", UpdatedAt: "now",
	})
	for _, agent := range store.AgentTypes {
		h.d.Config.SkillTargets = append(h.d.Config.SkillTargets, store.SkillTarget{SkillID: "skill-demo", AgentType: agent, Enabled: false})
	}
}

func TestMulticaSkillTogglePushesToLibrary(t *testing.T) {
	h := newAPIHarness(t)
	logPath := setFakeMultica(t, h)
	seedSkill(t, h)

	status, data := h.api("POST", "/api/skills/skill-demo/toggle", map[string]any{"agentType": "multica", "enabled": true})
	if status != 200 {
		t.Fatalf("toggle 失败: status=%d data=%v", status, data)
	}
	lines := fakeLogLines(t, logPath)
	// create 走内容文件通道；名称 = 安装目录名
	if countCalls(lines, func(tokens []string) bool {
		return containsToken(tokens, "skill") && containsToken(tokens, "create") && containsToken(tokens, "demo-skill")
	}) != 1 {
		t.Fatalf("应有一次 skill create demo-skill: %v", lines)
	}
	if countCalls(lines, func(tokens []string) bool {
		return containsToken(tokens, "--content-file")
	}) == 0 {
		t.Fatalf("create 应走 --content-file: %v", lines)
	}
	// 推送哈希记账
	if got := h.d.Config.SkillPushState["multica"]["skill-demo"]; got == "" {
		t.Fatalf("SkillPushState 未记账: %v", h.d.Config.SkillPushState)
	}
	// 有状态库真实存在
	fake := readFakeState(t, h)
	if len(fake.Skills) != 1 || fake.Skills[0].Name != "demo-skill" {
		t.Fatalf("fake 技能库应有 demo-skill: %v", fake.Skills)
	}

	// 停用：按名从库移除
	if status, _ := h.api("POST", "/api/skills/skill-demo/toggle", map[string]any{"agentType": "multica", "enabled": false}); status != 200 {
		t.Fatalf("停用失败")
	}
	lines = fakeLogLines(t, logPath)
	if countCalls(lines, func(tokens []string) bool {
		// skill delete <id>（排除 skill files delete）
		return containsToken(tokens, "skill") && containsToken(tokens, "delete") && !containsToken(tokens, "files")
	}) != 1 {
		t.Fatalf("应有一次 skill delete: %v", lines)
	}
	fake = readFakeState(t, h)
	if len(fake.Skills) != 0 {
		t.Fatalf("停用后技能库应为空: %v", fake.Skills)
	}
	if got := h.d.Config.SkillPushState["multica"]["skill-demo"]; got != "" {
		t.Fatalf("停用后推送哈希应清除: %v", h.d.Config.SkillPushState)
	}
}

// —— 备份恢复守卫 —— //

func TestMulticaRestoreRejected(t *testing.T) {
	h := newAPIHarness(t)
	status, _ := h.api("POST", "/api/backups/restore", map[string]any{"agentType": "multica", "name": "x.json"})
	if status != 400 {
		t.Fatalf("远端 agent 恢复备份应被拒绝: %d", status)
	}
}
