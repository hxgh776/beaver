package tui

// 无头渲染验证（VENU-16 验收证据）：模拟屏幕上走真实渲染路径，
// 断言各页面渲染出关键内容；按键驱动 CRUD 见 tui_e2e_test.go。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// sandbox 启动独立数据目录（不碰 ~/.beaver）。
func sandbox(t *testing.T) (home, agentHome string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	agentHome = filepath.Join(base, "agents")
	for _, dir := range []string{home, agentHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home, agentHome
}

func TestHeadlessStatusRenders(t *testing.T) {
	home, agentHome := sandbox(t)
	done := make(chan error, 1)
	var captures [][]string
	go func() {
		done <- Run(Options{
			HomeDir:   home,
			AgentHome: agentHome,
			SyncUI:    true,
			Snapshot:  "status",
			Capture:   &captures,
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("headless 渲染失败: %v", err)
		}
	case <-time.After(60 * time.Second):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("headless 渲染超时（疑似死锁）\n%s", buf[:n])
	}
	if len(captures) != 1 {
		t.Fatalf("快照数 = %d，期望 1", len(captures))
	}

	text := strings.Join(captures[0], "\n")
	compact := strings.NewReplacer(" ", "", "\u3000", "").Replace(text)
	for _, want := range []string{"概览", "本机Agent", "运行环境", "从现有agent配置导入", "首次使用", "daemon:本进程持有"} {
		if !strings.Contains(compact, want) {
			t.Errorf("概览页缺少关键内容 %q\n--- 屏幕文本 ---\n%s", want, text)
		}
	}
}

// TestHeadlessAllPagesRender 逐页渲染，断言每页关键内容（对应 Web 面板的 8 个页签）。
func TestHeadlessAllPagesRender(t *testing.T) {
	home, agentHome := sandbox(t)
	var captures [][]string
	err := Run(Options{
		HomeDir:   home,
		AgentHome: agentHome,
		SyncUI:    true,
		Snapshot:  "all",
		Capture:   &captures,
	})
	if err != nil {
		t.Fatalf("headless 渲染失败: %v", err)
	}
	if len(captures) != len(pageTitles) {
		t.Fatalf("快照数 = %d，期望 %d", len(captures), len(pageTitles))
	}
	type needle struct {
		page int
		text string
	}
	for _, n := range []needle{
		{0, "概览"},
		{0, "MCPserver"},
		{1, "MCP管理"},
		{2, "MCP发现"},
		{3, "Agent分发"},
		{3, "分组"},
		{4, "聚合网关"},
		{4, "令牌鉴权"},
		{5, "调用记录"},
		{6, "Skill管理"},
		{7, "运行参数"},
		{7, "备份"},
	} {
		text := strings.Join(captures[n.page], "\n")
		compact := strings.NewReplacer(" ", "", "\u3000", "").Replace(text)
		if !strings.Contains(compact, n.text) {
			t.Errorf("页 %d 缺少关键内容 %q\n--- 屏幕文本 ---\n%s", n.page, n.text, text)
		}
	}
}

// TestHeadlessServerCRUDInUI 已删除（原实现只有 Run 快照 + 空断言，是空壳）。
// 真实的按键驱动 CRUD 全链路见 tui_e2e_test.go 的 TestTUIServerCRUDByKey；
// 按键级交互（侧栏切页/Ctrl+Q/模态取消/技能安装）同样在该文件。
