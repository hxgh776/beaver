package tui

// 按键驱动的 TUI 端到端测试（VENU-16 验收证据）：
// 在模拟屏幕上运行真实事件循环，注入键盘事件走「新增 MCP server → 表格出现 → 落盘 → 删除」
// 全流程，证明核心增删改查确实在 TUI 内可完成（而非仅编译通过）。
//
// 交互路径（与真实键盘一致）：
//   F2 → MCP 管理（空表回车）→ 模板选择（回车选空白）→ 表单（Tab 切字段、输入、回车保存）
//   → 表格渲染新行 → 方向键选中 → Delete → 确认框回车 → 列表消失且配置文件同步。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// e2eEnv 启动 app + 模拟屏幕 + 真实事件循环。
type e2eEnv struct {
	a      *App
	screen tcell.SimulationScreen
	w, h   int
}

func startE2E(t *testing.T) *e2eEnv {
	t.Helper()
	home, agentHome := sandbox(t)
	b, err := boot(Options{HomeDir: home, AgentHome: agentHome, Port: intPtr(0)})
	if err != nil {
		t.Fatalf("daemon 启动失败: %v", err)
	}
	a := newApp(b, true) // SyncUI：页面数据加载与测试 goroutine 同步
	a.buildLayout()

	sim := tcell.NewSimulationScreen("UTF-8")
	sim.SetSize(120, 40) // SetScreen 内部会 Init，勿手动调用（双重初始化会破坏模拟屏幕）
	a.tapp.SetScreen(sim)
	a.tapp.SetRoot(a.root, true) // 与 runHeadless 同序：屏幕就位后再挂根
	t.Cleanup(func() {
		a.tapp.Stop()
		b.shutdown()
	})

	loopErr := make(chan error, 1)
	go func() { loopErr <- a.tapp.Run() }()
	synced := make(chan struct{})
	go func() { a.ui(func() {}); close(synced) }()
	select {
	case <-synced:
	case <-time.After(10 * time.Second):
		t.Fatal("事件循环未能启动")
	}
	return &e2eEnv{a: a, screen: sim, w: 120, h: 40}
}

func intPtr(v int) *int { return &v }

// key 注入一次按键。
func (e *e2eEnv) key(key tcell.Key, r rune) {
	_ = e.a.tapp.QueueEvent(tcell.NewEventKey(key, r, tcell.ModNone))
}

// typeText 逐字符注入（rune 键）。
func (e *e2eEnv) typeText(s string) {
	for _, r := range s {
		e.key(tcell.KeyRune, r)
	}
}

// text 当前屏幕文本。
func (e *e2eEnv) text() string {
	lines := ScreenText(e.screen, e.w, e.h)
	return strings.Join(lines, "\n")
}

func (e *e2eEnv) compact() string {
	return strings.NewReplacer(" ", "", "\u3000", "").Replace(e.text())
}

// waitForText 轮询等待屏幕出现 needle（最多 timeout）。
func (e *e2eEnv) waitForText(t *testing.T, needle string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	needle = strings.ReplaceAll(needle, " ", "")
	for time.Now().Before(deadline) {
		compact := e.compact()
		if strings.Contains(compact, needle) {
			return e.text()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待屏幕出现 %q 超时\n--- 当前屏幕 ---\n%s", needle, e.text())
	return ""
}

// waitForGone 轮询等待屏幕消失 needle。
func (e *e2eEnv) waitForGone(t *testing.T, needle string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	needle = strings.ReplaceAll(needle, " ", "")
	for time.Now().Before(deadline) {
		if !strings.Contains(e.compact(), needle) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待屏幕消失 %q 超时\n--- 当前屏幕 ---\n%s", needle, e.text())
}

// TestTUIServerCRUDByKey 核心链路：新增（表单提交）→ 列表渲染 → 配置落盘 → 删除。
func TestTUIServerCRUDByKey(t *testing.T) {
	e := startE2E(t)

	// 进入 MCP 管理页（F2），焦点落在 server 表格（当前为空表）
	e.key(tcell.KeyF2, 0)
	e.waitForText(t, "MCP管理", 5*time.Second)
	e.waitForText(t, "目标", 5*time.Second) // 页面数据已套用（表格表头渲染）后再按键

	// 空表回车 = 新增 → 模板选择；回车选「空白（手填）」→ 表单
	e.key(tcell.KeyEnter, 0)
	e.waitForText(t, "选择模板", 5*time.Second)
	time.Sleep(200 * time.Millisecond) // 模态焦点切换落定
	e.key(tcell.KeyEnter, 0)
	e.waitForText(t, "新增MCPserver", 5*time.Second)
	time.Sleep(200 * time.Millisecond) // 表单焦点落定

	// 表单字段顺序：ID0 名称1 传输2 命令3 工作目录4 URL5 网关模式6 启用7 并发8 参数9 env10 headers11
	// 焦点初始在 ID（留空自动生成）→ Tab 到名称
	e.key(tcell.KeyTab, 0)
	e.typeText("e2e测试服务器")
	e.key(tcell.KeyTab, 0) // → 传输（下拉，跳过）
	e.key(tcell.KeyTab, 0) // → 命令
	e.typeText("npx")
	e.key(tcell.KeyTab, 0) // → 工作目录
	e.typeText(".")
	// 工作目录 → URL → 网关模式（默认勾选）→ 启用（默认勾选）→ 并发 → 参数（多行框）→
	// 表单层拦截把多行框内的 Tab 直接跳到「保存」
	for i := 0; i < 6; i++ {
		e.key(tcell.KeyTab, 0)
	}
	e.key(tcell.KeyEnter, 0) // 保存

	// 保存成功：模态关闭（状态栏闪出「已添加」），表格渲染新行
	e.waitForText(t, "已添加", 15*time.Second)
	e.waitForGone(t, "新增MCPserver", 5*time.Second)
	e.waitForText(t, "e2e测试服务器", 10*time.Second)

	// 配置确已落盘（daemon 持久化 config.json）
	data, err := os.ReadFile(filepath.Join(e.a.b.homeDir, "config.json"))
	if err != nil {
		t.Fatalf("读取 config.json 失败: %v", err)
	}
	if !strings.Contains(string(data), "e2e") {
		t.Fatalf("config.json 未包含新 server\n%s", data)
	}

	// 删除：模态已关、焦点回到表格 → 选中第一行 → Delete → 确认框回车
	e.key(tcell.KeyDown, 0)
	e.key(tcell.KeyDelete, 0)
	e.waitForText(t, "删除MCPserver", 5*time.Second)
	time.Sleep(200 * time.Millisecond) // 确认框焦点落定
	e.key(tcell.KeyEnter, 0)           // 确认
	e.waitForText(t, "已删除", 15*time.Second)

	// 列表中消失 + 落盘数据消失
	e.waitForGone(t, "e2e测试服务器", 15*time.Second)
	data, err = os.ReadFile(filepath.Join(e.a.b.homeDir, "config.json"))
	if err != nil {
		t.Fatalf("读取 config.json 失败: %v", err)
	}
	if strings.Contains(string(data), "e2e-") || strings.Contains(string(data), "e2e测试服务器") {
		t.Fatalf("config.json 仍包含已删除 server\n%s", data)
	}
}
