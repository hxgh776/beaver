// 同步引擎对拍测试：移植自 packages/core/test/sync.test.ts（U-IM / C1 C5 / I-SY / M3.7）。
package syncengine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hxgh776/beaver/internal/adapters"
	"github.com/hxgh776/beaver/internal/coreerr"
	"github.com/hxgh776/beaver/internal/jsonv"
	"github.com/hxgh776/beaver/internal/registry"
	"github.com/hxgh776/beaver/internal/store"
	"github.com/hxgh776/beaver/internal/syncengine"
)

type harness struct {
	t       *testing.T
	sandbox string
	agents  map[string]registry.AgentRef
	store   *store.Store
	engine  syncengine.Options
	config  *store.Config
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	sandbox := t.TempDir()
	h := &harness{
		t:       t,
		sandbox: sandbox,
		agents:  syncengine.DefaultAgentRefs(sandbox),
		store:   store.NewStore(sandbox),
	}
	h.engine = syncengine.Options{
		BackupsDir: h.store.BackupsDir(),
		Agents:     h.agents,
		StdioBridge: &syncengine.StdioBridge{
			Command:  "node",
			BaseArgs: []string{"C:/tools/beaver/bridge-main.js"},
		},
	}
	config, err := h.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	h.config = config
	return h
}

func (h *harness) addStdioServer(id, name string) {
	cmd := "npx"
	args := []string{"-y", fmt.Sprintf("@modelcontextprotocol/server-%s", id)}
	env := map[string]string{"TOKEN": fmt.Sprintf("secret-%s", id)}
	registry.UpsertServer(h.config, registry.UpsertServerInput{
		ID: id, Name: name, Transport: store.TransportStdio, Command: &cmd, Args: &args, Env: &env,
	})
}

func (h *harness) writeAgentFile(t *testing.T, rel []string, content string) {
	t.Helper()
	file := filepath.Join(h.sandbox, filepath.Join(rel...))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) readJSON(t *testing.T, rel []string) *jsonv.Value {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.sandbox, filepath.Join(rel...)))
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsonv.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// —— detect + import (U-IM / C1 C5) —— //

func TestDetectOnlyExisting(t *testing.T) {
	h := newHarness(t)
	if err := os.MkdirAll(filepath.Join(h.sandbox, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(h.sandbox, ".cursor", "mcp.json"),
		[]byte(`{"mcpServers":{"ctx":{"command":"node","args":["ctx.js"]}}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	detections := registry.DetectAgents(h.agents)
	byType := map[string]registry.AgentDetection{}
	for _, d := range detections {
		byType[d.AgentType] = d
	}
	cursor := byType[store.AgentCursor]
	if !cursor.Detected {
		t.Fatal("cursor 应被探测到")
	}
	want := filepath.Join(h.sandbox, ".cursor", "mcp.json")
	if len(cursor.ConfigPaths) != 1 || cursor.ConfigPaths[0] != want {
		t.Fatalf("cursor configPaths 不符: %v", cursor.ConfigPaths)
	}
	claude := byType[store.AgentClaudeCode]
	if claude.Detected {
		t.Fatal("claude-code 不应被探测到")
	}
	wantClaude := filepath.Join(h.sandbox, ".claude.json")
	if len(claude.ConfigPaths) != 1 || claude.ConfigPaths[0] != wantClaude {
		t.Fatalf("claude configPaths 不符: %v", claude.ConfigPaths)
	}
}

func TestImportFingerprintDedupe(t *testing.T) {
	h := newHarness(t)
	if err := os.MkdirAll(filepath.Join(h.sandbox, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(h.sandbox, ".cursor", "mcp.json"),
		[]byte(`{"mcpServers":{"fs":{"command":"npx","args":["-y","srv-fs"]}}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(h.sandbox, ".claude.json"),
		[]byte(`{"mcpServers":{"fs2":{"command":"npx","args":["-y","srv-fs"]},"gh":{"command":"node","args":["gh.js"]}}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	groups, err := registry.ImportPreview(h.agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 { // srv-fs 两个 agent 同指纹 → 1 组；gh 1 组
		t.Fatalf("分组数应为 2: %d", len(groups))
	}
	var fsGroup *registry.ImportCandidateGroup
	for i := range groups {
		if strings.Contains(groups[i].Fingerprint, "srv-fs") {
			fsGroup = &groups[i]
		}
	}
	if fsGroup == nil || len(fsGroup.Candidates) != 2 {
		t.Fatalf("srv-fs 组候选应为 2: %+v", fsGroup)
	}

	added := registry.ApplyImport(h.config, groups, nil)
	if len(added) != 2 {
		t.Fatalf("应新增 2 个: %d", len(added))
	}
	// 分组的 id 取首个候选（AGENT_LIST 顺序 claude-code 在前 → fs2）
	ids := []string{}
	for _, s := range h.config.Servers {
		ids = append(ids, s.ID)
	}
	slices.Sort(ids)
	if slices.Equal(ids, []string{"fs2", "gh"}) == false {
		t.Fatalf("ids 不符: %v", ids)
	}

	// 再导一次：指纹重复 → 0 新增
	groups2, err := registry.ImportPreview(h.agents)
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.ApplyImport(h.config, groups2, nil); len(got) != 0 {
		t.Fatalf("重复导入应 0 新增: %d", len(got))
	}
}

func TestImportExcludesGatewayKeys(t *testing.T) {
	h := newHarness(t)
	if err := os.MkdirAll(filepath.Join(h.sandbox, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(h.sandbox, ".cursor", "mcp.json"),
		[]byte(`{"mcpServers":{"beaver-gateway":{"url":"http://127.0.0.1:6280/mcp","headers":{"Authorization":"Bearer x"}},"agent-control-gateway":{"url":"http://127.0.0.1:6280/mcp","headers":{"Authorization":"Bearer x"}},"real":{"command":"node","args":["real.js"]}}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	groups, err := registry.ImportPreview(h.agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("分组数应为 1: %d", len(groups))
	}
	if groups[0].Suggested.Name != "real" {
		t.Fatalf("应为 real: %s", groups[0].Suggested.Name)
	}
}

// —— sync engine (I-SY) —— //

func TestSyncBoundMatrixThreeAgents(t *testing.T) {
	h := newHarness(t)
	h.writeAgentFile(t, []string{".claude.json"}, `{"theme":"dark","mcpServers":{"user1":{"command":"x"}}}`)
	h.writeAgentFile(t, []string{".codex", "config.toml"}, "# my config\nmodel = \"x\"\n")
	h.writeAgentFile(t, []string{".cursor", "mcp.json"}, `{"mcpServers":{}}`)

	h.addStdioServer("filesystem", "Filesystem")
	_ = registry.SetBinding(h.config, "filesystem", store.AgentClaudeCode)
	_ = registry.SetBinding(h.config, "filesystem", store.AgentCodex)
	_ = registry.SetBinding(h.config, "filesystem", store.AgentCursor)

	report, err := syncengine.Sync(h.config, h.engine, syncengine.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Save(h.config); err != nil {
		t.Fatal(err)
	}

	claude := h.readJSON(t, []string{".claude.json"})
	theme, _ := claude.Object().Get("theme")
	if theme.String() != "dark" {
		t.Fatalf("theme 应保留: %v", theme.String())
	}
	mcpServers, _ := claude.Object().Get("mcpServers")
	user1, _ := mcpServers.Object().Get("user1")
	if jsonv.Canonical(user1) != `{"command":"x"}` {
		t.Fatalf("user1 应保留: %s", jsonv.Canonical(user1))
	}
	fs, _ := mcpServers.Object().Get("filesystem")
	fsCmd, _ := fs.Object().Get("command")
	if fsCmd.String() != "npx" {
		t.Fatalf("filesystem.command 不符: %v", fsCmd.String())
	}
	fsEnv, _ := fs.Object().Get("env")
	tok, _ := fsEnv.Object().Get("TOKEN")
	if tok.String() != "secret-filesystem" {
		t.Fatalf("env.TOKEN 不符: %v", tok.String())
	}

	codexText, _ := os.ReadFile(filepath.Join(h.sandbox, ".codex", "config.toml"))
	if !strings.Contains(string(codexText), "# my config") {
		t.Fatal("codex 注释应保留")
	}
	if !strings.Contains(string(codexText), "[mcp_servers.filesystem]") {
		t.Fatal("codex 缺少托管段")
	}

	cursor := h.readJSON(t, []string{".cursor", "mcp.json"})
	cursorServers, _ := cursor.Object().Get("mcpServers")
	cursorFs, _ := cursorServers.Object().Get("filesystem")
	cursorCmd, _ := cursorFs.Object().Get("command")
	if cursorCmd.String() != "npx" {
		t.Fatalf("cursor filesystem.command 不符: %v", cursorCmd.String())
	}

	// 报告：三个绑定的 agent 各一个 upsert（serverId 附加）
	for _, agentType := range []string{store.AgentClaudeCode, store.AgentCodex, store.AgentCursor} {
		var r *syncengine.AgentSyncReport
		for i := range report.PerAgent {
			if report.PerAgent[i].AgentType == agentType {
				r = &report.PerAgent[i]
			}
		}
		found := false
		for _, c := range r.Changes {
			if c.Key == "filesystem" && c.Action == "upsert" && c.ServerID != nil && *c.ServerID == "filesystem" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s 报告缺 filesystem upsert: %+v", agentType, r.Changes)
		}
	}

	// 幂等：再同步全部 no-change
	report2, err := syncengine.Sync(h.config, h.engine, syncengine.Input{})
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range report2.PerAgent {
		for _, c := range agent.Changes {
			if c.Action != "none" {
				t.Fatalf("幂等重放应为全 none: %+v", agent.Changes)
			}
		}
	}
}

func TestSyncUnbindConvergesRemoval(t *testing.T) {
	h := newHarness(t)
	h.addStdioServer("filesystem", "Filesystem")
	_ = registry.SetBinding(h.config, "filesystem", store.AgentCursor)
	if _, err := syncengine.Sync(h.config, h.engine, syncengine.Input{}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Save(h.config); err != nil {
		t.Fatal(err)
	}
	cursor := h.readJSON(t, []string{".cursor", "mcp.json"})
	cursorServers, _ := cursor.Object().Get("mcpServers")
	if _, ok := cursorServers.Object().Get("filesystem"); !ok {
		t.Fatal("首次同步应写入 filesystem")
	}

	kept := h.config.Bindings[:0]
	for _, b := range h.config.Bindings {
		if !(b.ServerID == "filesystem" && b.AgentType == store.AgentCursor) {
			kept = append(kept, b)
		}
	}
	h.config.Bindings = kept
	report, err := syncengine.Sync(h.config, h.engine, syncengine.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Save(h.config); err != nil {
		t.Fatal(err)
	}
	var cursorReport *syncengine.AgentSyncReport
	for i := range report.PerAgent {
		if report.PerAgent[i].AgentType == store.AgentCursor {
			cursorReport = &report.PerAgent[i]
		}
	}
	foundRemove := false
	for _, c := range cursorReport.Changes {
		if c.Key == "filesystem" && c.Action == "remove" && c.ServerID == nil {
			foundRemove = true
		}
	}
	if !foundRemove {
		t.Fatalf("应有 remove 变更（serverId 为空）: %+v", cursorReport.Changes)
	}
	doc := h.readJSON(t, []string{".cursor", "mcp.json"})
	docServers, _ := doc.Object().Get("mcpServers")
	if _, ok := docServers.Object().Get("filesystem"); ok {
		t.Fatal("filesystem 应被收敛移除")
	}
}

func TestSyncGatewayMode(t *testing.T) {
	h := newHarness(t)
	h.writeAgentFile(t, []string{".codex", "config.toml"}, "# my config\n")
	h.addStdioServer("notion", "Notion")
	h.config.Servers[0].GatewayMode = true
	_ = registry.SetBinding(h.config, "notion", store.AgentClaudeCode)
	_ = registry.SetBinding(h.config, "notion", store.AgentCodex)

	report, err := syncengine.Sync(h.config, h.engine, syncengine.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Save(h.config); err != nil {
		t.Fatal(err)
	}

	claude := h.readJSON(t, []string{".claude.json"})
	mcpServers, _ := claude.Object().Get("mcpServers")
	if _, ok := mcpServers.Object().Get("notion"); ok {
		t.Fatal("notion 不应直连写入")
	}
	// D7 默认关闭令牌 → 网关条目不带凭证
	gw, _ := mcpServers.Object().Get(store.GatewayKey)
	typ, _ := gw.Object().Get("type")
	url, _ := gw.Object().Get("url")
	if typ.String() != "http" {
		t.Fatalf("type 不符: %v", typ.String())
	}
	wantURL := fmt.Sprintf("http://127.0.0.1:%d/agents/claude-code/mcp", h.config.Settings.Port)
	if url.String() != wantURL {
		t.Fatalf("url 不符: %v", url.String())
	}
	if _, hasHeaders := gw.Object().Get("headers"); hasHeaders {
		t.Fatal("默认关闭令牌时不应有 headers")
	}

	codexText, _ := os.ReadFile(filepath.Join(h.sandbox, ".codex", "config.toml"))
	if !strings.Contains(string(codexText), "[mcp_servers.beaver-gateway]") {
		t.Fatal("codex 缺少网关段")
	}
	if !strings.Contains(string(codexText), "bridge-main.js") {
		t.Fatal("codex 缺少桥命令")
	}
	if !strings.Contains(string(codexText), fmt.Sprintf("http://127.0.0.1:%d/agents/codex/mcp", h.config.Settings.Port)) {
		t.Fatal("codex 缺少网关 URL")
	}
	if strings.Contains(string(codexText), "--token=") {
		t.Fatal("默认关闭令牌时不应有 --token")
	}
	var codexReport *syncengine.AgentSyncReport
	for i := range report.PerAgent {
		if report.PerAgent[i].AgentType == store.AgentCodex {
			codexReport = &report.PerAgent[i]
		}
	}
	if len(codexReport.Unsupported) != 0 {
		t.Fatalf("codex 不应有 unsupported: %+v", codexReport.Unsupported)
	}

	// 开启令牌 → 重写后带凭证（claude 带 Authorization；codex 桥带 --token）
	auth := true
	h.config.Settings.AuthRequired = &auth
	if _, err := syncengine.Sync(h.config, h.engine, syncengine.Input{}); err != nil {
		t.Fatal(err)
	}
	claude2 := h.readJSON(t, []string{".claude.json"})
	mcp2, _ := claude2.Object().Get("mcpServers")
	gw2, _ := mcp2.Object().Get(store.GatewayKey)
	headers, _ := gw2.Object().Get("headers")
	authHeader, _ := headers.Object().Get("Authorization")
	if authHeader.String() != "Bearer "+h.config.Settings.Token {
		t.Fatalf("Authorization 不符: %v", authHeader.String())
	}
	codex2, _ := os.ReadFile(filepath.Join(h.sandbox, ".codex", "config.toml"))
	if !strings.Contains(string(codex2), "--token="+h.config.Settings.Token) {
		t.Fatal("codex 桥应带 --token")
	}
}

// 占位（消除无关变量告警）：报告遍历在上方用例内完成
func report0() []syncengine.AgentSyncReport { return nil }

func TestSyncDirectHTTPViaBridge(t *testing.T) {
	h := newHarness(t)
	h.writeAgentFile(t, []string{".codex", "config.toml"}, "# my config\n")
	url := "https://api.example.com/mcp"
	headers := map[string]string{"X-API-Key": "k-1"}
	registry.UpsertServer(h.config, registry.UpsertServerInput{
		ID: "web-api", Name: "Web API", Transport: store.TransportHTTP, URL: &url, Headers: &headers,
	})
	_ = registry.SetBinding(h.config, "web-api", store.AgentCodex)
	report, err := syncengine.Sync(h.config, h.engine, syncengine.Input{})
	if err != nil {
		t.Fatal(err)
	}
	var codexReport *syncengine.AgentSyncReport
	for i := range report.PerAgent {
		if report.PerAgent[i].AgentType == store.AgentCodex {
			codexReport = &report.PerAgent[i]
		}
	}
	if len(codexReport.Unsupported) != 0 {
		t.Fatalf("不应有 unsupported: %+v", codexReport.Unsupported)
	}
	codexText, _ := os.ReadFile(filepath.Join(h.sandbox, ".codex", "config.toml"))
	if !strings.Contains(string(codexText), "[mcp_servers.web-api]") {
		t.Fatal("缺 web-api 段")
	}
	if !strings.Contains(string(codexText), "https://api.example.com/mcp") {
		t.Fatal("缺 URL")
	}
	if !strings.Contains(string(codexText), "--header") || !strings.Contains(string(codexText), "X-API-Key=k-1") {
		t.Fatal("缺 --header 注入")
	}
}

func TestSyncConflictAndOverride(t *testing.T) {
	h := newHarness(t)
	h.writeAgentFile(t, []string{".cursor", "mcp.json"}, `{"mcpServers":{}}`)
	h.addStdioServer("filesystem", "Filesystem")
	_ = registry.SetBinding(h.config, "filesystem", store.AgentCursor)
	if _, err := syncengine.Sync(h.config, h.engine, syncengine.Input{}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Save(h.config); err != nil {
		t.Fatal(err)
	}

	// 用户手改
	cursorFile := filepath.Join(h.sandbox, ".cursor", "mcp.json")
	doc := h.readJSON(t, []string{".cursor", "mcp.json"})
	mcpServers, _ := doc.Object().Get("mcpServers")
	fs, _ := mcpServers.Object().Get("filesystem")
	fs.Object().Set("args", jsonv.NewArray(jsonv.NewString("-y"), jsonv.NewString("hacked")))
	if err := os.WriteFile(cursorFile, []byte(jsonv.StringifyCompact(doc)), 0o644); err != nil {
		t.Fatal(err)
	}

	conflict, err := syncengine.Sync(h.config, h.engine, syncengine.Input{})
	if err != nil {
		t.Fatal(err)
	}
	var cr *syncengine.AgentSyncReport
	for i := range conflict.PerAgent {
		if conflict.PerAgent[i].AgentType == store.AgentCursor {
			cr = &conflict.PerAgent[i]
		}
	}
	if len(cr.Conflicts) != 1 {
		t.Fatalf("应有一个冲突: %+v", cr.Conflicts)
	}
	if cr.Conflicts[0].ResolutionKey != "cursor:filesystem" {
		t.Fatalf("resolutionKey 不符: %v", cr.Conflicts[0].ResolutionKey)
	}

	overrideReport, err := syncengine.Sync(h.config, h.engine, syncengine.Input{
		Resolutions: map[string]adapters.ConflictResolution{"cursor:filesystem": adapters.ResolutionOverride},
	})
	if err != nil {
		t.Fatal(err)
	}
	var or *syncengine.AgentSyncReport
	for i := range overrideReport.PerAgent {
		if overrideReport.PerAgent[i].AgentType == store.AgentCursor {
			or = &overrideReport.PerAgent[i]
		}
	}
	if or.Conflicts[0].Resolution != adapters.ResolutionOverride {
		t.Fatalf("应为 override: %+v", or.Conflicts[0])
	}
	after := h.readJSON(t, []string{".cursor", "mcp.json"})
	mcp2, _ := after.Object().Get("mcpServers")
	fs2, _ := mcp2.Object().Get("filesystem")
	args2, _ := fs2.Object().Get("args")
	want := jsonv.NewArray(jsonv.NewString("-y"), jsonv.NewString("@modelcontextprotocol/server-filesystem"))
	if jsonv.Canonical(args2) != jsonv.Canonical(want) {
		t.Fatalf("override 后 args 不符: %s", jsonv.Canonical(args2))
	}
}

func TestSyncCreatesBackups(t *testing.T) {
	h := newHarness(t)
	h.writeAgentFile(t, []string{".claude.json"}, `{"mcpServers":{}}`)
	h.addStdioServer("filesystem", "Filesystem")
	_ = registry.SetBinding(h.config, "filesystem", store.AgentClaudeCode)
	if _, err := syncengine.Sync(h.config, h.engine, syncengine.Input{}); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(h.store.BackupsDir(), store.AgentClaudeCode)
	files, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("应有一个备份: %d", len(files))
	}
	if !regexp.MustCompile(`claude\.json$`).MatchString(files[0].Name()) {
		t.Fatalf("备份名不符: %s", files[0].Name())
	}
}

// —— ZCode 嵌套容器 + WorkBuddy 适配（M3.7） —— //

func TestZcodeNestedReadWrite(t *testing.T) {
	h := newHarness(t)
	h.writeAgentFile(t, []string{".zcode", "cli", "config.json"}, `{
  "mcp": {"servers": {"user-own": {"type": "http", "url": "https://example.com/mcp"}}, "otherSetting": {"keep": true}},
  "plugins": {"some-plugin": true},
  "hooks": {"enabled": false}
}`)
	// 导入预览能读到嵌套容器里的条目
	groups, err := registry.ImportPreview(h.agents)
	if err != nil {
		t.Fatal(err)
	}
	zcodeCandidates := 0
	for _, g := range groups {
		for _, c := range g.Candidates {
			if c.AgentType == store.AgentZCode {
				zcodeCandidates++
			}
		}
	}
	if zcodeCandidates != 1 {
		t.Fatalf("zcode 候选应为 1: %d", zcodeCandidates)
	}

	h.addStdioServer("filesystem", "Filesystem")
	_ = registry.SetBinding(h.config, "filesystem", store.AgentZCode)
	if _, err := syncengine.Sync(h.config, h.engine, syncengine.Input{}); err != nil {
		t.Fatal(err)
	}

	after := h.readJSON(t, []string{".zcode", "cli", "config.json"})
	mcp, _ := after.Object().Get("mcp")
	servers, _ := mcp.Object().Get("servers")
	fsEntry, _ := servers.Object().Get("filesystem")
	cmd, _ := fsEntry.Object().Get("command")
	if cmd.String() != "npx" {
		t.Fatalf("filesystem.command 不符: %v", cmd.String())
	}
	// 只写官方字段（ZCode schema 严格，未知键会被客户端丢弃）
	if jsonv.Canonical(fsEntry) != `{"args":["-y","@modelcontextprotocol/server-filesystem"],"command":"npx","env":{"TOKEN":"secret-filesystem"}}` {
		t.Fatalf("filesystem 条目不符（只允许官方字段）: %s", jsonv.Canonical(fsEntry))
	}
	// 用户原有条目 + 嵌套兄弟键 + 顶层其他键全部保留
	userOwn, _ := servers.Object().Get("user-own")
	if jsonv.Canonical(userOwn) != `{"type":"http","url":"https://example.com/mcp"}` {
		t.Fatalf("user-own 应保留: %s", jsonv.Canonical(userOwn))
	}
	other, _ := mcp.Object().Get("otherSetting")
	if jsonv.Canonical(other) != `{"keep":true}` {
		t.Fatal("otherSetting 应保留")
	}
	plugins, _ := after.Object().Get("plugins")
	if jsonv.Canonical(plugins) != `{"some-plugin":true}` {
		t.Fatal("plugins 应保留")
	}
	hooks, _ := after.Object().Get("hooks")
	if jsonv.Canonical(hooks) != `{"enabled":false}` {
		t.Fatal("hooks 应保留")
	}
}

func TestZcodeFirstWriteCreatesNested(t *testing.T) {
	h := newHarness(t)
	h.writeAgentFile(t, []string{".zcode", "cli", "config.json"}, `{"plugins":{"some-plugin":true},"hooks":{"enabled":false}}`)
	h.addStdioServer("filesystem", "Filesystem")
	_ = registry.SetBinding(h.config, "filesystem", store.AgentZCode)
	if _, err := syncengine.Sync(h.config, h.engine, syncengine.Input{}); err != nil {
		t.Fatal(err)
	}
	after := h.readJSON(t, []string{".zcode", "cli", "config.json"})
	mcp, _ := after.Object().Get("mcp")
	servers, _ := mcp.Object().Get("servers")
	fsEntry, _ := servers.Object().Get("filesystem")
	if jsonv.Canonical(fsEntry) != `{"args":["-y","@modelcontextprotocol/server-filesystem"],"command":"npx","env":{"TOKEN":"secret-filesystem"}}` {
		t.Fatalf("filesystem 不符: %s", jsonv.Canonical(fsEntry))
	}
	plugins, _ := after.Object().Get("plugins")
	if jsonv.Canonical(plugins) != `{"some-plugin":true}` {
		t.Fatal("plugins 应保留")
	}
}

func TestZcodeEmptyMcpAndMalformed(t *testing.T) {
	h := newHarness(t)
	h.writeAgentFile(t, []string{".zcode", "cli", "config.json"}, `{"mcp":{}}`)
	h.addStdioServer("filesystem", "Filesystem")
	_ = registry.SetBinding(h.config, "filesystem", store.AgentZCode)
	if _, err := syncengine.Sync(h.config, h.engine, syncengine.Input{}); err != nil {
		t.Fatal(err)
	}
	after := h.readJSON(t, []string{".zcode", "cli", "config.json"})
	mcp, _ := after.Object().Get("mcp")
	servers, _ := mcp.Object().Get("servers")
	fsEntry, _ := servers.Object().Get("filesystem")
	cmd, _ := fsEntry.Object().Get("command")
	if cmd.String() != "npx" {
		t.Fatalf("command 不符: %v", cmd.String())
	}

	broken := `{"mcp":"not-an-object"}`
	h.writeAgentFile(t, []string{".zcode", "cli", "config.json"}, broken)
	_, err := syncengine.Sync(h.config, h.engine, syncengine.Input{})
	var malformed *coreerr.MalformedConfigError
	isMalformed := false
	if err != nil {
		if _, ok := err.(*coreerr.MalformedConfigError); ok {
			isMalformed = true
			_ = malformed
		}
	}
	if !isMalformed {
		t.Fatalf("应为 MALFORMED_CONFIG: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(h.sandbox, ".zcode", "cli", "config.json"))
	if string(data) != broken {
		t.Fatalf("文件被动过: %q", data)
	}
}

func TestWorkbuddyRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.writeAgentFile(t, []string{".workbuddy", "mcp.json"}, `{"mcpServers":{"remote":{"disabled":false,"timeout":30000,"type":"streamableHttp","url":"https://light.example/mcp"}}}`)
	detections := registry.DetectAgents(h.agents)
	wbDetected := false
	for _, d := range detections {
		if d.AgentType == store.AgentWorkBuddy {
			wbDetected = d.Detected
		}
	}
	if !wbDetected {
		t.Fatal("workbuddy 应被探测到")
	}

	h.addStdioServer("filesystem", "Filesystem")
	_ = registry.SetBinding(h.config, "filesystem", store.AgentWorkBuddy)
	if _, err := syncengine.Sync(h.config, h.engine, syncengine.Input{}); err != nil {
		t.Fatal(err)
	}

	after := h.readJSON(t, []string{".workbuddy", "mcp.json"})
	mcpServers, _ := after.Object().Get("mcpServers")
	fsEntry, _ := mcpServers.Object().Get("filesystem")
	// 新写入的条目用 WorkBuddy 的 type 命名（stdio 无 type 字段）
	if jsonv.Canonical(fsEntry) != `{"args":["-y","@modelcontextprotocol/server-filesystem"],"command":"npx","env":{"TOKEN":"secret-filesystem"}}` {
		t.Fatalf("filesystem 不符: %s", jsonv.Canonical(fsEntry))
	}
	// 未管辖的既有条目原样保留（含 WorkBuddy 特有字段）
	remote, _ := mcpServers.Object().Get("remote")
	if jsonv.Canonical(remote) != `{"disabled":false,"timeout":30000,"type":"streamableHttp","url":"https://light.example/mcp"}` {
		t.Fatalf("remote 应原样保留: %s", jsonv.Canonical(remote))
	}
}
