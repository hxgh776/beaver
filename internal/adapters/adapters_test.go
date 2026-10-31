// 适配器对拍测试：移植自 packages/core/test/adapters.test.ts（U-AD / U-CF）
// 与 write-engine.test.ts 的 C6 嵌套容器 / 解析失败中止用例。
package adapters_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hxgh776/beaver/internal/adapters"
	"github.com/hxgh776/beaver/internal/coreerr"
	"github.com/hxgh776/beaver/internal/fsutil"
	"github.com/hxgh776/beaver/internal/jsonv"
	"github.com/hxgh776/beaver/internal/store"
)

var fixturesRoot = filepath.Join("testdata", "fixtures")

func copyFixture(t *testing.T, rel, destName string) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), destName)
	data, err := os.ReadFile(filepath.Join(fixturesRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dest
}

func fsEntry() *adapters.RawServerEntry {
	cmd := "npx"
	args := []string{"-y", "@modelcontextprotocol/server-filesystem", `D:\proj`}
	env := map[string]string{"FS_ROOT": `D:\proj`}
	return &adapters.RawServerEntry{
		Transport: store.TransportStdio,
		Command:   &cmd,
		Args:      args,
		Env:       env,
	}
}

func claudeAdapter(file string) adapters.Adapter {
	return adapters.NewJsonAgentAdapter(adapters.JsonAdapterOptions{
		Type:         store.AgentClaudeCode,
		DisplayName:  "Claude Code",
		Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP},
		Paths:        []string{file},
		ContainerKey: "mcpServers",
		Normalize:    adapters.NormalizeClaudeStyle,
		Denormalize:  adapters.DenormalizeClaudeStyle,
	})
}

func codexAdapter(file string) adapters.Adapter {
	return adapters.NewCodexTomlAdapter([]string{file})
}

func parseFile(t *testing.T, file string) *jsonv.Value {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsonv.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func readOne(t *testing.T, a adapters.Adapter, file, key string) adapters.RawServerEntry {
	t.Helper()
	entries, err := a.Read(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Key == key {
			return e.Entry
		}
	}
	t.Fatalf("键 %s 不存在", key)
	return adapters.RawServerEntry{}
}

// U-AD-01 读取：抽取 mcpServers 并归一化 stdio/http
func TestClaudeRead(t *testing.T) {
	file := copyFixture(t, "claude-code/claude.json", "claude.json")
	user := readOne(t, claudeAdapter(file), file, "user-server")
	if user.Transport != store.TransportStdio || deref(user.Command) != "uvx" {
		t.Fatalf("user-server 不符: %+v", user)
	}
	remote := readOne(t, claudeAdapter(file), file, "remote-api")
	if remote.Transport != store.TransportHTTP || deref(remote.URL) != "https://api.example.com/mcp" {
		t.Fatalf("remote-api 不符: %+v", remote)
	}
}

// U-AD-02 写入：只动 mcpServers，无关顶层键逐键深等
func TestClaudeWritePreservesUnrelated(t *testing.T) {
	file := copyFixture(t, "claude-code/claude.json", "claude.json")
	before := parseFile(t, file)
	result, err := claudeAdapter(file).Apply(file,
		[]adapters.FragmentWrite{{Key: "filesystem", Entry: fsEntry()}},
		adapters.ApplyOptions{Previous: []store.KeyState{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	after := parseFile(t, file)
	assertJSONPath(t, after, "theme", before)
	assertJSONPath(t, after, "numStartups", before)
	// TS 只断言受影响容器里的既有键 user-server 不变 + projects 顶层键
	assertJSONPath(t, after, "projects", before)
	beforeMcp, _ := before.Object().Get("mcpServers")
	afterMcp, _ := after.Object().Get("mcpServers")
	beforeUser, _ := beforeMcp.Object().Get("user-server")
	afterUser, _ := afterMcp.Object().Get("user-server")
	if jsonv.Canonical(afterUser) != jsonv.Canonical(beforeUser) {
		t.Fatal("user-server 应保持不变")
	}
	fs, _ := afterMcp.Object().Get("filesystem")
	fsCmd, _ := fs.Object().Get("command")
	if fsCmd.String() != "npx" {
		t.Fatalf("filesystem.command 不符: %v", fsCmd.String())
	}
	if len(result.KeyStates) != 1 || result.KeyStates[0].Key != "filesystem" || result.KeyStates[0].Hash == "" {
		t.Fatalf("keyStates 不符: %+v", result.KeyStates)
	}
}

// 辅助：断言两文档同键值相等（顶层）
func assertJSONPath(t *testing.T, after *jsonv.Value, key string, before *jsonv.Value) {
	t.Helper()
	av, _ := after.Object().Get(key)
	bv, _ := before.Object().Get(key)
	if jsonv.Canonical(av) != jsonv.Canonical(bv) {
		t.Fatalf("%s 应保持不变", key)
	}
}

// U-AD-03 删除：仅移除受管键，其余保留
func TestClaudeDeleteManagedOnly(t *testing.T) {
	file := copyFixture(t, "claude-code/claude.json", "claude.json")
	a := claudeAdapter(file)
	if _, err := a.Apply(file, []adapters.FragmentWrite{{Key: "filesystem", Entry: fsEntry()}}, adapters.ApplyOptions{Previous: []store.KeyState{}}); err != nil {
		t.Fatal(err)
	}
	doc := parseFile(t, file)
	container, _ := doc.Object().Get("mcpServers")
	target, _ := container.Object().Get("filesystem")
	hash := fsutil.StableHash(target)
	if _, err := a.Apply(file, []adapters.FragmentWrite{{Key: "filesystem"}}, adapters.ApplyOptions{
		Previous: []store.KeyState{{Key: "filesystem", Hash: hash}},
	}); err != nil {
		t.Fatal(err)
	}
	after := parseFile(t, file)
	container, _ = after.Object().Get("mcpServers")
	if _, ok := container.Object().Get("filesystem"); ok {
		t.Fatal("filesystem 应被删除")
	}
	if _, ok := container.Object().Get("user-server"); !ok {
		t.Fatal("user-server 应保留")
	}
	theme, _ := after.Object().Get("theme")
	if theme.String() != "dark" {
		t.Fatalf("theme 应保留 dark: %v", theme.String())
	}
}

// U-AD-04 坏 JSON：抛 MalformedConfigError 且原文件未动
func TestClaudeBadJSONAborts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(file, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := claudeAdapter(file).Apply(file,
		[]adapters.FragmentWrite{{Key: "x", Entry: fsEntry()}},
		adapters.ApplyOptions{Previous: []store.KeyState{}},
	)
	var malformed *coreerr.MalformedConfigError
	if !asMalformed(err) {
		t.Fatalf("应为 MalformedConfigError: %v", err)
	}
	_ = malformed
	data, _ := os.ReadFile(file)
	if string(data) != "{ not json" {
		t.Fatalf("原文件被动过: %q", data)
	}
}

func asMalformed(err error) bool {
	_, ok := err.(*coreerr.MalformedConfigError)
	return ok
}

// U-AD-05 备份回调：真实变更触发一次，无变更/dryRun 不触发
func TestClaudeBackupCallback(t *testing.T) {
	sandbox := t.TempDir()
	file := filepath.Join(sandbox, "claude2.json")
	if _, err := os.Stat(file); err != nil {
		data, _ := os.ReadFile(filepath.Join(fixturesRoot, "claude-code", "claude.json"))
		_ = os.WriteFile(file, data, 0o644)
	}
	a := claudeAdapter(file)
	backups := 0
	if _, err := a.Apply(file, []adapters.FragmentWrite{{Key: "filesystem", Entry: fsEntry()}}, adapters.ApplyOptions{
		Previous: []store.KeyState{},
		Backup:   func() error { backups++; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if backups != 1 {
		t.Fatalf("真实变更应触发一次备份: %d", backups)
	}
	if _, err := a.Apply(file, []adapters.FragmentWrite{{Key: "filesystem", Entry: fsEntry()}}, adapters.ApplyOptions{
		Previous: []store.KeyState{},
		Backup:   func() error { backups++; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if backups != 1 {
		t.Fatalf("幂等重放不应触发备份: %d", backups)
	}
	dryRunBackups := 0
	entry := &adapters.RawServerEntry{Transport: store.TransportStdio, Command: strp("x")}
	if _, err := a.Apply(file, []adapters.FragmentWrite{{Key: "other", Entry: entry}}, adapters.ApplyOptions{
		Previous: []store.KeyState{},
		DryRun:   true,
		Backup:   func() error { dryRunBackups++; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if dryRunBackups != 0 {
		t.Fatalf("dryRun 不应触发备份: %d", dryRunBackups)
	}
}

func strp(s string) *string { return &s }

// U-CF 上次写入的片段被手改 → 冲突默认跳过，override 才写入
func TestConflictSkipThenOverride(t *testing.T) {
	sandbox := t.TempDir()
	file := filepath.Join(sandbox, "claude3.json")
	copyFixtureTo(t, "claude-code/claude.json", file)
	a := claudeAdapter(file)
	first, err := a.Apply(file, []adapters.FragmentWrite{{Key: "filesystem", Entry: fsEntry()}}, adapters.ApplyOptions{Previous: []store.KeyState{}})
	if err != nil {
		t.Fatal(err)
	}
	var ks store.KeyState
	for _, k := range first.KeyStates {
		if k.Key == "filesystem" {
			ks = k
		}
	}

	// 用户手改
	doc := parseFile(t, file)
	container, _ := doc.Object().Get("mcpServers")
	target, _ := container.Object().Get("filesystem")
	target.Object().Set("args", jsonv.NewArray(jsonv.NewString("-y"), jsonv.NewString("hacked")))
	_ = os.WriteFile(file, []byte(jsonv.StringifyPretty(doc)+"\n"), 0o644)

	newArgs := []string{"-y", "@modelcontextprotocol/server-filesystem", `D:\new`}
	changed := &adapters.RawServerEntry{Transport: store.TransportStdio, Command: fsEntry().Command, Args: newArgs, Env: fsEntry().Env}
	skip, err := a.Apply(file, []adapters.FragmentWrite{{Key: "filesystem", Entry: changed}}, adapters.ApplyOptions{Previous: []store.KeyState{ks}})
	if err != nil {
		t.Fatal(err)
	}
	if len(skip.Conflicts) != 1 || skip.Conflicts[0].Resolution != adapters.ResolutionSkip {
		t.Fatalf("应有一个 skip 冲突: %+v", skip.Conflicts)
	}
	untouched := parseFile(t, file)
	c, _ := untouched.Object().Get("mcpServers")
	f, _ := c.Object().Get("filesystem")
	args, _ := f.Object().Get("args")
	if jsonv.Canonical(args) != `["-y","hacked"]` {
		t.Fatalf("手改内容应保留: %s", jsonv.Canonical(args))
	}

	override, err := a.Apply(file, []adapters.FragmentWrite{{Key: "filesystem", Entry: changed}}, adapters.ApplyOptions{
		Previous:    []store.KeyState{ks},
		Resolutions: map[string]adapters.ConflictResolution{"filesystem": adapters.ResolutionOverride},
	})
	if err != nil {
		t.Fatal(err)
	}
	if override.Conflicts[0].Resolution != adapters.ResolutionOverride {
		t.Fatalf("应为 override: %+v", override.Conflicts)
	}
	overridden := parseFile(t, file)
	c2, _ := overridden.Object().Get("mcpServers")
	f2, _ := c2.Object().Get("filesystem")
	args2, _ := f2.Object().Get("args")
	want := jsonv.NewArray(jsonv.NewString("-y"), jsonv.NewString("@modelcontextprotocol/server-filesystem"), jsonv.NewString(`D:\new`))
	if jsonv.Canonical(args2) != jsonv.Canonical(want) {
		t.Fatalf("override 后 args 不符: %s", jsonv.Canonical(args2))
	}
}

func copyFixtureTo(t *testing.T, rel, dest string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixturesRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// U-CF 删除无 previous 记录的键 → 视为冲突，绝不静默删除
func TestDeleteWithoutPreviousIsConflict(t *testing.T) {
	sandbox := t.TempDir()
	file := filepath.Join(sandbox, "claude4.json")
	copyFixtureTo(t, "claude-code/claude.json", file)
	result, err := claudeAdapter(file).Apply(file,
		[]adapters.FragmentWrite{{Key: "user-server"}},
		adapters.ApplyOptions{Previous: []store.KeyState{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range result.Conflicts {
		if c.Key == "user-server" {
			found = true
		}
	}
	if !found {
		t.Fatalf("user-server 应为冲突: %+v", result.Conflicts)
	}
	after := parseFile(t, file)
	container, _ := after.Object().Get("mcpServers")
	if _, ok := container.Object().Get("user-server"); !ok {
		t.Fatal("user-server 不应被删除")
	}
}

// U-AD-06 读取：解析 [mcp_servers.*]
func TestCodexRead(t *testing.T) {
	sandbox := t.TempDir()
	file := filepath.Join(sandbox, "config.toml")
	copyFixtureTo(t, "codex/config.toml", file)
	user := readOne(t, codexAdapter(file), file, "user_own")
	if user.Transport != store.TransportStdio || deref(user.Command) != "user-server" {
		t.Fatalf("user_own 不符: %+v", user)
	}
}

// U-AD-07 写入：非托管区注释与顺序字节级保留
func TestCodexWritePreservesUnmanagedBytes(t *testing.T) {
	sandbox := t.TempDir()
	file := filepath.Join(sandbox, "config.toml")
	copyFixtureTo(t, "codex/config.toml", file)
	before, _ := os.ReadFile(file)
	a := codexAdapter(file)
	entry := fsEntry()
	entry.Env = map[string]string{"API_KEY": "secret-1"}
	result, err := a.Apply(file, []adapters.FragmentWrite{{Key: "filesystem", Entry: entry}}, adapters.ApplyOptions{Previous: []store.KeyState{}})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(file)
	if len(result.Changes) != 1 || result.Changes[0].Key != "filesystem" || result.Changes[0].Action != "upsert" {
		t.Fatalf("changes 不符: %+v", result.Changes)
	}
	// 字节级：非托管区完全一致
	managed := map[string]struct{}{"filesystem": {}, "user_own": {}}
	if stripManagedSectionsPublic(string(after), managed) != stripManagedSectionsPublic(string(before), managed) {
		t.Fatal("非托管区应字节级一致")
	}
	for _, want := range []string{"# 用户自定义注释，必须原样保留", "# 托管区外的注释", "[profiles.fast]"} {
		if !strings.Contains(string(after), want) {
			t.Fatalf("应包含 %q", want)
		}
	}
	// 语义：新段可解析、env 保真
	servers := readOne(t, a, file, "filesystem")
	if deref(servers.Command) != "npx" || servers.Env["API_KEY"] != "secret-1" {
		t.Fatalf("filesystem 不符: %+v", servers)
	}
}

// U-AD-07b 幂等重放：同内容再次 apply 产生 no-change
func TestCodexIdempotentReplay(t *testing.T) {
	sandbox := t.TempDir()
	file := filepath.Join(sandbox, "config.toml")
	copyFixtureTo(t, "codex/config.toml", file)
	a := codexAdapter(file)
	write := []adapters.FragmentWrite{{Key: "filesystem", Entry: fsEntry()}}
	first, err := a.Apply(file, write, adapters.ApplyOptions{Previous: []store.KeyState{}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Apply(file, write, adapters.ApplyOptions{Previous: first.KeyStates})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Changes) != 1 || second.Changes[0].Key != "filesystem" || second.Changes[0].Action != "none" {
		t.Fatalf("重放应为 no-change: %+v", second.Changes)
	}
}

// U-AD-08 手改检测 + 删除保护
func TestCodexHandEditDetection(t *testing.T) {
	sandbox := t.TempDir()
	file := filepath.Join(sandbox, "config.toml")
	copyFixtureTo(t, "codex/config.toml", file)
	a := codexAdapter(file)
	first, err := a.Apply(file, []adapters.FragmentWrite{{Key: "filesystem", Entry: fsEntry()}}, adapters.ApplyOptions{Previous: []store.KeyState{}})
	if err != nil {
		t.Fatal(err)
	}
	var ks store.KeyState
	for _, k := range first.KeyStates {
		if k.Key == "filesystem" {
			ks = k
		}
	}
	doc, _ := os.ReadFile(file)
	if err := os.WriteFile(file, regexp.MustCompile(`"npx"`).ReplaceAll(doc, []byte(`"npx-hacked"`)), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := "bunx"
	changed := &adapters.RawServerEntry{Transport: store.TransportStdio, Command: &cmd, Args: fsEntry().Args, Env: fsEntry().Env}
	second, err := a.Apply(file, []adapters.FragmentWrite{{Key: "filesystem", Entry: changed}}, adapters.ApplyOptions{Previous: []store.KeyState{ks}})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Conflicts) != 1 {
		t.Fatalf("应有一个冲突: %+v", second.Conflicts)
	}
	after, _ := os.ReadFile(file)
	if !strings.Contains(string(after), `"npx-hacked"`) && !strings.Contains(string(after), "npx-hacked") {
		t.Fatalf("手改应保留: %q", after)
	}
	// 删除非本工具所写的键 → 冲突
	rm, err := a.Apply(file, []adapters.FragmentWrite{{Key: "user_own"}}, adapters.ApplyOptions{Previous: []store.KeyState{}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range rm.Conflicts {
		if c.Key == "user_own" {
			found = true
		}
	}
	if !found {
		t.Fatalf("user_own 删除应为冲突: %+v", rm.Conflicts)
	}
	if _, err := a.Read(file); err != nil {
		t.Fatal(err)
	}
	entries, _ := a.Read(file)
	exists := false
	for _, e := range entries {
		if e.Key == "user_own" {
			exists = true
		}
	}
	if !exists {
		t.Fatal("user_own 应保留")
	}
}

// U-AD-09/11 normalize/denormalize round-trips
func TestNormalizeDenormalizeRoundTrips(t *testing.T) {
	stdio := &adapters.RawServerEntry{Transport: store.TransportStdio, Command: strp("node"), Args: []string{"s.js"}, Env: map[string]string{"A": "1"}}
	httpEntry := &adapters.RawServerEntry{Transport: store.TransportHTTP, URL: strp("https://x/mcp"), Headers: map[string]string{"H": "v"}}

	s1, _ := adapters.DenormalizeUrlOrCommand(stdio)
	back1 := adapters.NormalizeUrlOrCommand(s1)
	if !reflect.DeepEqual(back1, stdio) {
		t.Fatalf("cursor stdio 往返不符: %+v vs %+v", back1, stdio)
	}
	h1, _ := adapters.DenormalizeUrlOrCommand(httpEntry)
	back2 := adapters.NormalizeUrlOrCommand(h1)
	if !reflect.DeepEqual(back2, httpEntry) {
		t.Fatalf("cursor http 往返不符: %+v vs %+v", back2, httpEntry)
	}

	local := &adapters.RawServerEntry{Transport: store.TransportStdio, Command: strp("npx"), Args: []string{"-y", "srv"}, Env: map[string]string{"KEY": "v"}}
	remote := &adapters.RawServerEntry{Transport: store.TransportHTTP, URL: strp("https://y/mcp")}
	l1, _ := adapters.DenormalizeOpencode(local)
	back3 := adapters.NormalizeOpencode(l1)
	if !reflect.DeepEqual(back3, local) {
		t.Fatalf("opencode local 往返不符: %+v vs %+v", back3, local)
	}
	r1, _ := adapters.DenormalizeOpencode(remote)
	back4 := adapters.NormalizeOpencode(r1)
	if !reflect.DeepEqual(back4, remote) {
		t.Fatalf("opencode remote 往返不符: %+v vs %+v", back4, remote)
	}
	// 与 fixture 中真实形态一致
	raw := jsonv.NewObject()
	raw.Set("type", jsonv.NewString("local"))
	raw.Set("command", jsonv.NewArray(jsonv.NewString("npx"), jsonv.NewString("-y"), jsonv.NewString("server-x")))
	raw.Set("environment", jsonv.NewObject())
	raw.Object().Set("KEY", jsonv.NewString("v")) // 注：environment 为对象
	// 重构：environment 应是对象
	envObj := jsonv.NewObject()
	envObj.Set("KEY", jsonv.NewString("v"))
	raw.Object().Set("environment", envObj)
	got := adapters.NormalizeOpencode(raw)
	wantEnv := map[string]string{"KEY": "v"}
	if got == nil || got.Transport != store.TransportStdio || deref(got.Command) != "npx" ||
		!reflect.DeepEqual(got.Args, []string{"-y", "server-x"}) || !reflect.DeepEqual(got.Env, wantEnv) {
		t.Fatalf("opencode 归一化不符: %+v", got)
	}
}

// 注册表：10 个 agent 均有适配器与候选路径
func TestAllAgentsHaveAdapters(t *testing.T) {
	for _, def := range adapters.AgentDefinitions("") {
		if len(def.Adapter.CandidatePaths()) == 0 {
			t.Fatalf("%s 无候选路径", def.Type)
		}
		if def.DisplayName == "" {
			t.Fatalf("%s 无展示名", def.Type)
		}
	}
	codex := adapters.MakeAgents("")[store.AgentCodex]
	tr := codex.Adapter.Transports()
	if len(tr) != 1 || tr[0] != store.TransportStdio {
		t.Fatalf("codex transports 应为 [stdio]: %v", tr)
	}
}

// dryRun 返回计划与预期 hash，不落盘
func TestJSONAdapterDryRun(t *testing.T) {
	sandbox := t.TempDir()
	file := filepath.Join(sandbox, "claude5.json")
	copyFixtureTo(t, "claude-code/claude.json", file)
	a := claudeAdapter(file)
	before, _ := os.ReadFile(file)
	result, err := a.Apply(file,
		[]adapters.FragmentWrite{
			{Key: "filesystem", Entry: fsEntry()},
			{Key: "user-server"},
		},
		adapters.ApplyOptions{Previous: []store.KeyState{}, DryRun: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	foundUpsert := false
	for _, c := range result.Changes {
		if c.Key == "filesystem" && c.Action == "upsert" {
			foundUpsert = true
		}
	}
	if !foundUpsert {
		t.Fatalf("changes 应含 filesystem upsert: %+v", result.Changes)
	}
	// user-server 非本工具所写（无 previous 记录）→ 按设计判为冲突，计划中为 no-op
	foundNone := false
	for _, c := range result.Changes {
		if c.Key == "user-server" && c.Action == "none" {
			foundNone = true
		}
	}
	if !foundNone {
		t.Fatalf("changes 应含 user-server none: %+v", result.Changes)
	}
	conflictFound := false
	for _, c := range result.Conflicts {
		if c.Key == "user-server" {
			conflictFound = true
		}
	}
	if !conflictFound {
		t.Fatalf("conflicts 应含 user-server: %+v", result.Conflicts)
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("dryRun 不应落盘")
	}
}

// —— write-engine 移植的 C6 / 解析失败中止用例 ——

func makeZcode(file string) adapters.Adapter {
	return adapters.NewJsonAgentAdapter(adapters.JsonAdapterOptions{
		Type:         store.AgentZCode,
		DisplayName:  "ZCode",
		Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP, store.TransportSSE},
		Paths:        []string{file},
		ContainerKey: "mcp.servers",
		Normalize:    adapters.NormalizeClaudeStyle,
		Denormalize:  adapters.DenormalizeClaudeStyle,
	})
}

func writeAgentFile(t *testing.T, sandbox string, rel []string, content string) string {
	t.Helper()
	file := filepath.Join(sandbox, filepath.Join(rel...))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// C6：首次写入新建中间层（无 mcp 键）→ 校验通过不回滚
func TestNestedContainerFirstWrite(t *testing.T) {
	sandbox := t.TempDir()
	file := writeAgentFile(t, sandbox, []string{".zcode", "cli", "config.json"}, `{"plugins":{"a":true}}`)
	url := "http://127.0.0.1:6280/mcp"
	result, err := makeZcode(file).Apply(file,
		[]adapters.FragmentWrite{{Key: "beaver", Entry: &adapters.RawServerEntry{Transport: store.TransportHTTP, URL: &url}}},
		adapters.ApplyOptions{Previous: []store.KeyState{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Key != "beaver" || result.Changes[0].Action != "upsert" {
		t.Fatalf("changes 不符: %+v", result.Changes)
	}
	after := parseFile(t, file)
	mcp, _ := after.Object().Get("mcp")
	servers, _ := mcp.Object().Get("servers")
	beaver, _ := servers.Object().Get("beaver")
	typ, _ := beaver.Object().Get("type")
	u, _ := beaver.Object().Get("url")
	if typ.String() != "http" || u.String() != url {
		t.Fatalf("beaver 条目不符: %s %s", typ.String(), u.String())
	}
	plugins, _ := after.Object().Get("plugins")
	if jsonv.Canonical(plugins) != `{"a":true}` {
		t.Fatalf("plugins 应保留: %s", jsonv.Canonical(plugins))
	}
}

// C6：中间层已有兄弟键 → 摘叶后不误剪，校验通过
func TestNestedContainerWithSibling(t *testing.T) {
	sandbox := t.TempDir()
	file := writeAgentFile(t, sandbox, []string{".zcode", "cli", "config.json"}, `{"mcp":{"other":{"keep":1}}}`)
	if _, err := makeZcode(file).Apply(file,
		[]adapters.FragmentWrite{{Key: "beaver", Entry: &adapters.RawServerEntry{Transport: store.TransportStdio, Command: strp("npx")}}},
		adapters.ApplyOptions{Previous: []store.KeyState{}},
	); err != nil {
		t.Fatal(err)
	}
	after := parseFile(t, file)
	mcp, _ := after.Object().Get("mcp")
	other, _ := mcp.Object().Get("other")
	if jsonv.Canonical(other) != `{"keep":1}` {
		t.Fatalf("mcp.other 应保留: %s", jsonv.Canonical(other))
	}
	servers, _ := mcp.Object().Get("servers")
	beaver, _ := servers.Object().Get("beaver")
	if cmd, _ := beaver.Object().Get("command"); cmd.String() != "npx" {
		t.Fatalf("beaver.command 不符: %v", cmd.String())
	}
}

// C6：中间层是非对象 → MALFORMED_CONFIG 且文件一个字节不动
func TestNestedContainerNonObjectAborts(t *testing.T) {
	sandbox := t.TempDir()
	raw := `{"mcp":"not-an-object"}`
	file := writeAgentFile(t, sandbox, []string{".zcode", "cli", "config.json"}, raw)
	_, err := makeZcode(file).Apply(file,
		[]adapters.FragmentWrite{{Key: "beaver", Entry: &adapters.RawServerEntry{Transport: store.TransportStdio, Command: strp("npx")}}},
		adapters.ApplyOptions{Previous: []store.KeyState{}},
	)
	if !asMalformed(err) {
		t.Fatalf("应为 MalformedConfigError: %v", err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != raw {
		t.Fatalf("文件被动过: %q", data)
	}
}

// U-WE-03 JSON 适配器：损坏文件 apply 直接中止，内容一个字节不动
func TestBrokenJSONAbortsByteUntouched(t *testing.T) {
	sandbox := t.TempDir()
	raw := "{broken json!!"
	file := writeAgentFile(t, sandbox, []string{".cursor", "mcp.json"}, raw)
	a := adapters.NewJsonAgentAdapter(adapters.JsonAdapterOptions{
		Type:         store.AgentCursor,
		DisplayName:  "Cursor",
		Transports:   []store.Transport{store.TransportStdio, store.TransportHTTP, store.TransportSSE},
		Paths:        []string{file},
		ContainerKey: "mcpServers",
		Normalize:    adapters.NormalizeClaudeStyle,
		Denormalize: func(e *adapters.RawServerEntry) (*jsonv.Value, error) {
			o := jsonv.NewObject()
			if e.Command != nil {
				o.Set("command", jsonv.NewString(*e.Command))
			}
			return o, nil
		},
	})
	_, err := a.Apply(file, []adapters.FragmentWrite{{Key: "a", Entry: &adapters.RawServerEntry{Transport: store.TransportStdio, Command: strp("npx")}}}, adapters.ApplyOptions{Previous: []store.KeyState{}})
	if !asMalformed(err) {
		t.Fatalf("应为 MalformedConfigError: %v", err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != raw {
		t.Fatalf("文件被动过: %q", data)
	}
}

// U-WE-03 TOML 适配器：损坏文件 apply 直接中止
func TestBrokenTOMLAbortsByteUntouched(t *testing.T) {
	sandbox := t.TempDir()
	raw := "[mcp_servers.a\nbroken===\n"
	file := writeAgentFile(t, sandbox, []string{".codex", "config.toml"}, raw)
	_, err := adapters.NewCodexTomlAdapter([]string{file}).Apply(file,
		[]adapters.FragmentWrite{{Key: "b", Entry: &adapters.RawServerEntry{Transport: store.TransportStdio, Command: strp("npx")}}},
		adapters.ApplyOptions{Previous: []store.KeyState{}},
	)
	if !asMalformed(err) {
		t.Fatalf("应为 MalformedConfigError: %v", err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != raw {
		t.Fatalf("文件被动过: %q", data)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func stripManagedSectionsPublic(text string, managed map[string]struct{}) string {
	return adapters.StripManagedSections(text, managed)
}
