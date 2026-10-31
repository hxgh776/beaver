package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// U-ST-02 save → load 往返一致
func TestStoreRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	config, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	cmd := "npx"
	args := []string{"-y", "@modelcontextprotocol/server-filesystem", `D:\tmp`}
	config.Servers = append(config.Servers, &ServerDef{
		ID:        "filesystem",
		Name:      "Filesystem",
		Transport: TransportStdio,
		Command:   &cmd,
		Args:      &args,
		// Env nil → 输出中不含 env 键；往返后仍无
		GatewayMode: false,
		Enabled:     true,
		CreatedAt:   "2026-01-01T00:00:00.000Z",
		UpdatedAt:   "2026-01-01T00:00:00.000Z",
	})
	if err := s.Save(config); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(s.HomeDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Servers) != len(config.Servers) {
		t.Fatalf("server 数不符: %d vs %d", len(reloaded.Servers), len(config.Servers))
	}
	a, _ := json.Marshal(reloaded.Servers)
	b, _ := json.Marshal(config.Servers)
	if string(a) != string(b) {
		t.Fatalf("往返不一致:\n%s\n%s", a, b)
	}
}

// U-ST-02 缺省配置：token 已生成、端口 6280
func TestDefaultConfig(t *testing.T) {
	config, err := NewStore(t.TempDir()).Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.Settings.Port != 6280 {
		t.Fatalf("端口应为 6280: %d", config.Settings.Port)
	}
	if !regexp.MustCompile(`^[0-9a-f]{48}$`).MatchString(config.Settings.Token) {
		t.Fatalf("token 应为 48 位小写十六进制: %s", config.Settings.Token)
	}
	if config.Version != 1 {
		t.Fatalf("version 应为 1: %d", config.Version)
	}
	// .default([])：缺省字段迁移为空数组（输出为 [] 而非 null）
	data, err := os.ReadFile(NewStore(t.TempDir()).ConfigPath())
	_ = data
	cfg2 := DefaultConfig()
	data, _ = json.Marshal(cfg2)
	if regexp.MustCompile(`"groups":\s*null`).Match(data) {
		t.Fatal("groups 不应序列化为 null")
	}
}

// U-ST-03 config.json 损坏 → 移为 .corrupt-* 并重建默认配置
func TestCorruptConfigRebuilds(t *testing.T) {
	sandbox := t.TempDir()
	s := NewStore(sandbox)
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
	withServer, _ := s.Load()
	cmd := "foo"
	withServer.Servers = append(withServer.Servers, &ServerDef{
		ID: "x", Name: "X", Transport: TransportStdio, Command: &cmd,
		GatewayMode: false, Enabled: true,
		CreatedAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z",
	})
	if err := s.Save(withServer); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.ConfigPath(), []byte("{ not valid json !!"), 0o644); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Servers) != 0 {
		t.Fatalf("损坏后应重建默认配置: %d", len(recovered.Servers))
	}
	entries, _ := os.ReadDir(sandbox)
	found := false
	for _, e := range entries {
		if regexp.MustCompile(`^config\.json\.corrupt-\d+$`).MatchString(e.Name()) {
			found = true
		}
	}
	if !found {
		t.Fatal("未找到 .corrupt- 留证文件")
	}
}

// U-ST-03 校验失败（结构非法）同样触发保护
func TestInvalidStructureRebuilds(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := os.WriteFile(s.ConfigPath(), []byte(`{"version":2,"servers":"nope"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.Version != 1 {
		t.Fatalf("损坏后应重建 version=1: %d", config.Version)
	}
}

// U-ST-07 resolveHomeDir 优先级
func TestResolveHomeDirPrecedence(t *testing.T) {
	home, _ := os.UserHomeDir()
	t.Setenv("BEAVER_HOME", "")
	t.Setenv("MCP_MANAGER_HOME", "")

	t.Setenv("AGENT_CONTROL_HOME", `D:\sandbox-home`)
	if got := ResolveHomeDir(`D:\explicit`); got != `D:\explicit` {
		t.Fatalf("显式参数应优先: %q", got)
	}
	if got := ResolveHomeDir(""); got != `D:\sandbox-home` {
		t.Fatalf("环境变量次之: %q", got)
	}
	t.Setenv("AGENT_CONTROL_HOME", "")
	if got := ResolveHomeDir(""); got != filepath.Join(home, ".beaver") {
		t.Fatalf("缺省应为 ~/.beaver: %q", got)
	}
}

// U-ST-07 旧环境变量名 MCP_MANAGER_HOME 仍被兼容（升级过渡期）
func TestLegacyEnvName(t *testing.T) {
	t.Setenv("BEAVER_HOME", "")
	t.Setenv("AGENT_CONTROL_HOME", "")
	t.Setenv("MCP_MANAGER_HOME", "D:/legacy-home")
	if got := ResolveHomeDir(""); got != "D:/legacy-home" {
		t.Fatalf("旧环境变量应被兼容: %q", got)
	}
}

// maybeMigrateHome（数据目录迁移）
func TestMaybeMigrateHome(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "legacy-home")
	next := filepath.Join(base, "new-home")
	if err := os.Mkdir(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.json"), []byte(`{"marker":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MaybeMigrateHome(legacy, next); got != next {
		t.Fatalf("应迁移到新目录: %q", got)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("旧目录应已让位")
	}
	data, err := os.ReadFile(filepath.Join(next, "config.json"))
	if err != nil || string(data) != `{"marker":true}` {
		t.Fatalf("迁移内容不符: %s %v", data, err)
	}
	// 再跑一次幂等
	if got := MaybeMigrateHome(legacy, next); got != next {
		t.Fatalf("应幂等: %q", got)
	}
	// 新目录已存在：不动
	occupied := filepath.Join(base, "occupied")
	if err := os.Mkdir(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "config.json"), []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MaybeMigrateHome(legacy, occupied); got != occupied {
		t.Fatalf("新目录占用时应沿用: %q", got)
	}
	// 新目录存在但为空（被空跑创建）→ 让位迁移
	legacy2 := filepath.Join(base, "legacy-2")
	emptyNew := filepath.Join(base, "empty-new")
	if err := os.Mkdir(legacy2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy2, "config.json"), []byte(`{"marker":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(emptyNew, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := MaybeMigrateHome(legacy2, emptyNew); got != emptyNew {
		t.Fatalf("空新目录应让位迁移: %q", got)
	}
	data, err = os.ReadFile(filepath.Join(emptyNew, "config.json"))
	if err != nil || string(data) != `{"marker":2}` {
		t.Fatalf("迁移内容不符: %s %v", data, err)
	}
}
