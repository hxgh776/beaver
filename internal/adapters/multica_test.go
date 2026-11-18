package adapters_test

// MulticaAdapter（CLI 外置型 agent）单元测试：经真实 exec 通道（multicafake 重入）
// 验证 Apply 的库编排——add / 哈希跳过 / remove / 多余条目收敛 / dry-run 不发调用。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxgh776/beaver/internal/adapters"
	"github.com/hxgh776/beaver/internal/multica"
	"github.com/hxgh776/beaver/internal/multica/multicafake"
	"github.com/hxgh776/beaver/internal/store"
)

func TestMain(m *testing.M) {
	if os.Getenv(multicafake.Env) == "1" {
		os.Exit(multicafake.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// newFakeMulticaAdapter 指向测试二进制自举的 fake CLI（有状态库 + 调用日志）。
func newFakeMulticaAdapter(t *testing.T) (adapters.Adapter, string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("解析测试二进制失败: %v", err)
	}
	sandbox := t.TempDir()
	logPath := filepath.Join(sandbox, "fakecli.log")
	statePath := filepath.Join(sandbox, "fakestate.json")
	t.Setenv(multicafake.Env, "1")
	t.Setenv(multicafake.EnvLog, logPath)
	t.Setenv(multicafake.EnvState, statePath)
	a := adapters.NewMulticaAdapter()
	a.SetSource(func() store.MulticaSettings {
		return store.MulticaSettings{Binary: exe, WorkspaceID: "ws-1"}
	})
	return a, logPath, statePath
}

func httpEntry(url string) *adapters.RawServerEntry {
	return &adapters.RawServerEntry{Transport: store.TransportHTTP, URL: &url}
}

func fakeCallLines(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 一次调用都没发生（dry-run 的成功形态）
		}
		t.Fatalf("读 fake 日志: %v", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func countArgvLine(lines []string, keyword string) int {
	n := 0
	for _, line := range lines {
		if !strings.Contains(line, "config=") && !strings.Contains(line, "content=") && strings.Contains(line, keyword) {
			n++
		}
	}
	return n
}

func TestMulticaAdapterApplyAddThenHashSkip(t *testing.T) {
	a, logPath, _ := newFakeMulticaAdapter(t)
	writes := []adapters.FragmentWrite{{Key: "demo", Entry: httpEntry("https://mcp.example")}}

	res, err := a.Apply("multica://workspace", writes, adapters.ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.KeyStates) != 1 || res.KeyStates[0].Key != "demo" || res.KeyStates[0].Hash == "" {
		t.Fatalf("KeyStates 记账错误: %+v", res.KeyStates)
	}
	if len(res.Changes) != 1 || res.Changes[0].Action != "upsert" {
		t.Fatalf("Changes 错误: %+v", res.Changes)
	}
	if countArgvLine(fakeCallLines(t, logPath), "workspace\x1fmcp\x1fadd") != 1 {
		t.Fatalf("应有一次 add 调用")
	}

	// 同内容重放：哈希一致 → no-op（不再发 add）
	res2, err := a.Apply("multica://workspace", writes, adapters.ApplyOptions{Previous: res.KeyStates})
	if err != nil {
		t.Fatalf("Apply2: %v", err)
	}
	if len(res2.Changes) != 1 || res2.Changes[0].Action != "none" {
		t.Fatalf("同内容应 no-op: %+v", res2.Changes)
	}
	if countArgvLine(fakeCallLines(t, logPath), "workspace\x1fmcp\x1fadd") != 1 {
		t.Fatalf("no-op 不应再发 add")
	}
}

func TestMulticaAdapterApplyRemoveAndConvergeExtras(t *testing.T) {
	a, logPath, statePath := newFakeMulticaAdapter(t)
	// 预置库：linear（beaver 不再管理 → 收敛删除）+ manual（多余 → 收敛删除）
	seed := `{"servers":[{"id":"srv-1","name":"linear","transport":"http"},{"id":"srv-2","name":"manual-entry","transport":"http"}],"skills":[]}`
	if err := os.WriteFile(statePath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	// 只管理一个不存在于库的 server：add demo；linear/manual 均为多余 → 删除
	writes := []adapters.FragmentWrite{{Key: "demo", Entry: httpEntry("https://mcp.example")}}
	res, err := a.Apply("multica://workspace", writes, adapters.ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	lines := fakeCallLines(t, logPath)
	if countArgvLine(lines, "workspace\x1fmcp\x1fremove") != 2 {
		t.Fatalf("应收敛删除两个多余条目: %v", lines)
	}
	if len(res.KeyStates) != 1 || res.KeyStates[0].Key != "demo" {
		t.Fatalf("KeyStates 应只含受管键: %+v", res.KeyStates)
	}

	// 解绑：previous 里的 demo 被引擎补删除写 → remove
	res2, err := a.Apply("multica://workspace",
		[]adapters.FragmentWrite{{Key: "demo"}},
		adapters.ApplyOptions{Previous: res.KeyStates})
	if err != nil {
		t.Fatalf("Apply2: %v", err)
	}
	if len(res2.KeyStates) != 0 {
		t.Fatalf("删除后 KeyStates 应为空: %+v", res2.KeyStates)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"name"`) {
		t.Fatalf("库应被收敛为空: %s", string(data))
	}
}

func TestMulticaAdapterDryRunMakesNoCalls(t *testing.T) {
	a, logPath, _ := newFakeMulticaAdapter(t)
	writes := []adapters.FragmentWrite{
		{Key: "demo", Entry: httpEntry("https://mcp.example")},
		{Key: "gone"}, // previous 无记录 → dry-run 不展示
	}
	res, err := a.Apply("multica://workspace", writes, adapters.ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Changes) != 1 || res.Changes[0].Action != "upsert" {
		t.Fatalf("dry-run 计划错误: %+v", res.Changes)
	}
	if lines := fakeCallLines(t, logPath); len(lines) != 0 {
		t.Fatalf("dry-run 不应发起 CLI 调用: %v", lines)
	}
}

func TestMulticaAdapterFailureBubblesAsTypedError(t *testing.T) {
	a, _, _ := newFakeMulticaAdapter(t)
	t.Setenv(multicafake.EnvFail, "Admin or owner role required")
	_, err := a.Apply("multica://workspace",
		[]adapters.FragmentWrite{{Key: "demo", Entry: httpEntry("https://mcp.example")}},
		adapters.ApplyOptions{})
	if err == nil {
		t.Fatal("远端失败应返回错误")
	}
	var me *multica.Error
	if !asMulticaError(err, &me) || me.Code != multica.CodePermission {
		t.Fatalf("应保留 multica 失败分类: %v", err)
	}
}

func asMulticaError(err error, target **multica.Error) bool {
	for e := err; e != nil; {
		if me, ok := e.(*multica.Error); ok {
			*target = me
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

func TestMulticaAdapterDetectionAndBasics(t *testing.T) {
	a, _, _ := newFakeMulticaAdapter(t)
	if !a.(interface{ IsRemote() bool }).IsRemote() {
		t.Fatal("multica 应为远端型适配器")
	}
	detected, location := a.(interface {
		DetectRemote() (bool, string)
	}).DetectRemote()
	if !detected || location != "multica://workspace" {
		t.Fatalf("探测错误: %v %q", detected, location)
	}
	if got := a.CandidatePaths(); len(got) != 1 || got[0] != "multica://workspace" {
		t.Fatalf("CandidatePaths 应为虚拟路径: %v", got)
	}
	if entries, err := a.Read("multica://workspace"); err != nil || len(entries) != 0 {
		t.Fatalf("Read 应恒空: %v %v", entries, err)
	}

	// CLI 缺失 → 未探测到
	a2 := adapters.NewMulticaAdapter()
	t.Setenv("PATH", t.TempDir())
	detected2, _ := a2.DetectRemote()
	if detected2 {
		t.Fatal("CLI 缺失时应未探测到")
	}
}
