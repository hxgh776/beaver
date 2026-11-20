package tui

// 按键驱动的 TUI 端到端测试（VENU-16 验收证据）：
// 在模拟屏幕上运行真实事件循环，注入键盘事件走「新增 MCP server → 表格出现 → 落盘 → 删除」
// 全流程，证明核心增删改查确实在 TUI 内可完成（而非仅编译通过）。
//
// 交互路径（与真实键盘一致）：
//   F2 → MCP 管理（空表回车）→ 模板选择（回车选空白）→ 表单（Tab 切字段、输入、回车保存）
//   → 表格渲染新行 → 方向键选中 → Delete → 确认框回车 → 列表消失且配置文件同步。

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/hxgh776/beaver/internal/skills"
	"github.com/hxgh776/beaver/internal/store"
)

// e2eEnv 启动 app + 模拟屏幕 + 真实事件循环。
type e2eEnv struct {
	a       *App
	screen  tcell.SimulationScreen
	w, h    int
	loopErr chan error // 事件循环退出码（Stop 后可读；测试 Ctrl+Q 用）
}

func startE2E(t *testing.T) *e2eEnv {
	return startE2EWith(t, Options{}, true)
}

// startE2EWith 可注入 Options（测试桩）并选择同步模式：
// syncUI=true 供测试 goroutine 直接驱动数据加载（确定性快照）；
// syncUI=false 模拟真实交互（页面数据后台异步加载，断言靠轮询）——按键回调全部跑在事件循环上。
func startE2EWith(t *testing.T, extra Options, syncUI bool) *e2eEnv {
	t.Helper()
	home, agentHome := sandbox(t)
	extra.HomeDir = home
	extra.AgentHome = agentHome
	extra.Port = intPtr(0)
	b, err := boot(extra)
	if err != nil {
		t.Fatalf("daemon 启动失败: %v", err)
	}
	a := newApp(b, syncUI)
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
	return &e2eEnv{a: a, screen: sim, w: 120, h: 40, loopErr: loopErr}
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

// focus 经事件循环设置焦点（测试前置准备；后续按键仍是真实键盘事件）。
func (e *e2eEnv) focus(p tview.Primitive) {
	e.a.ui(func() { e.a.tapp.SetFocus(p) })
	time.Sleep(200 * time.Millisecond) // 焦点落定
}

// serverPage / skillsPage / settingsPage 取页面结构体（驱动工具栏按钮与表单焦点）。
func (e *e2eEnv) serverPage() *serversPage   { return e.a.pageStruct("servers").(*serversPage) }
func (e *e2eEnv) skillsPage() *skillsPage    { return e.a.pageStruct("skills").(*skillsPage) }
func (e *e2eEnv) settingsPage() *settingsPage { return e.a.pageStruct("settings").(*settingsPage) }

// TestTUISidebarEnterSwitchesPage 侧栏回车切页（复核复现路径：Ctrl+P → Enter → selectPage）。
// 修复前：selectPage 在事件循环内同步 QueueUpdate → 整个界面永久冻结（本用例 waitForText 超时）。
func TestTUISidebarEnterSwitchesPage(t *testing.T) {
	e := startE2EWith(t, Options{}, false)

	// Ctrl+P 聚焦侧栏（纯键盘），下移一行到「MCP 管理」，回车切页
	e.key(tcell.KeyCtrlP, 0)
	time.Sleep(200 * time.Millisecond)
	e.key(tcell.KeyDown, 0)
	e.key(tcell.KeyEnter, 0)

	// 切页成功 = 事件循环存活且 selectPage 非阻塞路径生效（页面边框标题为该页独有文案）
	e.waitForText(t, "回车编辑/新增", 5*time.Second)
	// 循环继续响应：回概览页再断言（死锁场景下此处必然超时）
	e.key(tcell.KeyF1, 0)
	e.waitForText(t, "重新探测", 5*time.Second)
}

// TestTUIQuitConfirmExits Ctrl+Q → 确认框 → 确认退出（复核复现路径：Ctrl+Q → confirm → a.ui）。
// 修复前：确认框弹不出、UI 冻结只能杀进程；修复后确认框可弹出且确认后事件循环正常退出。
func TestTUIQuitConfirmExits(t *testing.T) {
	e := startE2EWith(t, Options{}, false)

	e.key(tcell.KeyCtrlQ, 0)
	e.waitForText(t, "退出Beaver面板", 5*time.Second) // 确认框渲染出来（未冻结）
	time.Sleep(200 * time.Millisecond)               // 模态焦点落定
	e.key(tcell.KeyEnter, 0)                          // 确认（第一个按钮）

	select {
	case err := <-e.loopErr:
		if err != nil {
			t.Fatalf("事件循环异常退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("确认退出后事件循环 5s 未返回（疑似冻结）\n%s", buf[:n])
	}
}

// TestTUIModalCancelNoFreeze 编辑表单「取消」按钮（事件循环内直接 closeModal）：
// 打开新增表单 → 焦点交给「取消」→ 回车 → 模态关闭、无数据写入、循环存活。
func TestTUIModalCancelNoFreeze(t *testing.T) {
	e := startE2EWith(t, Options{}, false)

	e.key(tcell.KeyF2, 0)
	e.waitForText(t, "目标", 5*time.Second) // 页面数据加载完成
	e.key(tcell.KeyEnter, 0)               // 空表回车 → 模板选择
	e.waitForText(t, "选择模板", 5*time.Second)
	time.Sleep(200 * time.Millisecond)
	e.key(tcell.KeyEnter, 0) // 选「空白（手填）」→ 新增表单
	e.waitForText(t, "新增MCPserver", 5*time.Second)

	// 焦点交给表单「取消」按钮（第 2 个按钮），回车激活
	e.focus(e.serverPage().editForm.GetButton(1))
	e.key(tcell.KeyEnter, 0)

	e.waitForGone(t, "新增MCPserver", 5*time.Second)
	// 无数据写入
	servers, err := e.a.b.client.Servers()
	if err != nil {
		t.Fatalf("读取 servers 失败: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("取消后不应产生 server: %+v", servers)
	}
	// 循环存活：回概览页
	e.key(tcell.KeyF1, 0)
	e.waitForText(t, "重新探测", 5*time.Second)
}

// writeSkillZip 生成技能 ZIP（entries: 相对路径 → 内容）。
func writeSkillZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entries[n])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

const skillMDTemplate = "---\nname: %s\ndescription: e2e 测试技能\n---\n\n# %s\n\n测试用技能。\n"

// waitForSkill 轮询 daemon REST 直到目标技能出现，并校验 SSOT 目录落盘。
func waitForSkill(t *testing.T, e *e2eEnv, name string) skills.WithStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		list, _, err := e.a.b.client.Skills()
		if err == nil {
			for _, sk := range list {
				if sk.Name == name {
					md := filepath.Join(e.a.b.homeDir, "skills", sk.Directory, "SKILL.md")
					if _, err := os.Stat(md); err != nil {
						t.Fatalf("技能已注册但 SSOT 缺失 %s: %v", md, err)
					}
					return sk
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("安装超时：技能 %s 未出现在 daemon 技能列表", name)
	return skills.WithStatus{}
}

// pressInstallInPickEnableFor 在「选择分发目标」模态内把焦点 Tab 到「安装」并回车。
// 模态只列本地目录投影目标（远端型 multica 不参与安装期选择），数量 = SkillAgentDirs。
func pressInstallInPickEnableFor(e *e2eEnv) {
	for i := 0; i < len(skills.SkillAgentDirs); i++ {
		e.key(tcell.KeyTab, 0) // 逐个复选框后移
	}
	e.key(tcell.KeyEnter, 0) // 聚焦到第一个按钮「安装」并激活
}

// TestTUISkillInstallZipByKey 本地 ZIP 安装：安装按钮 → 不冻结 → 真实落盘。
func TestTUISkillInstallZipByKey(t *testing.T) {
	e := startE2EWith(t, Options{}, false)
	zipPath := filepath.Join(t.TempDir(), "e2e-skill.zip")
	writeSkillZip(t, zipPath, map[string]string{
		"e2e-zip-skill/SKILL.md": fmt_SkillMD("e2e-zip-skill"),
	})

	e.key(tcell.KeyF7, 0)
	e.waitForText(t, "搜索安装", 5*time.Second) // Skill 管理页
	e.focus(e.skillsPage().zipBtn)
	e.key(tcell.KeyEnter, 0)                    // 打开「本地 ZIP 安装」
	e.waitForText(t, "ZIP路径", 5*time.Second) // 模态独有文案（避免匹配到工具栏同名按钮）

	e.typeText(zipPath)      // 焦点初始在 ZIP 路径输入框
	e.key(tcell.KeyTab, 0)   // → 「安装（单技能）」按钮
	e.key(tcell.KeyEnter, 0) // 激活 → pickEnableFor
	e.waitForText(t, "选择分发目标", 5*time.Second) // 修复前：安装按钮全部死锁，此步必超时
	pressInstallInPickEnableFor(e)

	e.waitForGone(t, "选择分发目标", 10*time.Second)
	sk := waitForSkill(t, e, "e2e-zip-skill")
	if sk.Source != "local" {
		t.Fatalf("技能来源应为 local: %+v", sk)
	}
}

// TestTUISkillInstallCollectionByKey 仓库集合安装：安装集合按钮 → 不冻结 → 仓库内全部技能落盘。
func TestTUISkillInstallCollectionByKey(t *testing.T) {
	// FetchZip 桩：任意 GitHub 归档 URL 返回内存 ZIP（离线 e2e，走真实 daemon REST）
	e := startE2EWith(t, Options{SkillsFetchZip: func(ctx context.Context, url string) ([]byte, error) {
		tmp := t.TempDir()
		zp := filepath.Join(tmp, "stub.zip")
		writeSkillZip(t, zp, map[string]string{
			"demo-collection-main/alpha/SKILL.md": fmt_SkillMD("e2e-alpha"),
			"demo-collection-main/beta/SKILL.md":  fmt_SkillMD("e2e-beta"),
		})
		return os.ReadFile(zp)
	}}, false)

	e.key(tcell.KeyF7, 0)
	e.waitForText(t, "搜索安装", 5*time.Second)
	e.focus(e.skillsPage().collBtn)
	e.key(tcell.KeyEnter, 0)                     // 打开「仓库集合安装」
	e.waitForText(t, "仓库坐标", 5*time.Second) // 模态独有文案

	e.typeText("e2e-stub/demo-collection") // 仓库坐标（焦点初始在输入框 0）
	e.key(tcell.KeyTab, 0)                 // → 分支（留空 = main）
	e.key(tcell.KeyTab, 0)                 // → 「安装集合」按钮
	e.key(tcell.KeyEnter, 0)
	e.waitForText(t, "选择分发目标", 5*time.Second)
	pressInstallInPickEnableFor(e)

	e.waitForText(t, "集合安装完成：成功2个", 20*time.Second)
	waitForSkill(t, e, "e2e-alpha")
	waitForSkill(t, e, "e2e-beta")
}

// TestTUISkillInstallSearchByKey skills.sh 搜索安装：搜索 → 结果表回车 → 安装按钮 → 真实安装。
func TestTUISkillInstallSearchByKey(t *testing.T) {
	// skills.sh 搜索桩：返回一条指向桩仓库的技能行（source = owner/repo@branch）；
	// 归档下载走 FetchZip 桩
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"count":1,"skills":[{"skillId":"e2e-search-skill","name":"e2e-search-skill","installs":42,"source":"e2e-stub/demo-search@main"}]}`))
	}))
	defer stub.Close()
	e := startE2EWith(t, Options{
		SkillsShBase: stub.URL,
		SkillsFetchZip: func(ctx context.Context, url string) ([]byte, error) {
			tmp := t.TempDir()
			zp := filepath.Join(tmp, "stub.zip")
			writeSkillZip(t, zp, map[string]string{
				"demo-search-main/e2e-search-skill/SKILL.md": fmt_SkillMD("e2e-search-skill"),
			})
			return os.ReadFile(zp)
		},
	}, false)

	e.key(tcell.KeyF7, 0)
	e.waitForText(t, "搜索安装", 5*time.Second)
	e.focus(e.skillsPage().searchBtn)
	e.key(tcell.KeyEnter, 0) // 打开「skills.sh 搜索」
	e.waitForText(t, "skills.sh搜索", 5*time.Second)

	e.typeText("e2e")       // 焦点初始在关键词输入框
	e.key(tcell.KeyTab, 0)  // → 「搜索」按钮
	e.key(tcell.KeyEnter, 0)
	e.waitForText(t, "e2e-search-skill", 10*time.Second) // 结果渲染且焦点已交给结果表
	e.key(tcell.KeyDown, 0) // 选中第一行
	e.key(tcell.KeyEnter, 0) // 结果表回车 = 安装所选
	e.waitForText(t, "选择分发目标", 5*time.Second)
	pressInstallInPickEnableFor(e)

	// 安装完成：模态关闭 + 技能真实出现在 daemon REST 且 SSOT 落盘
	// （状态栏 flash 会被 120 列截断，不作为断言依据）
	e.waitForGone(t, "选择分发目标", 10*time.Second)
	waitForSkill(t, e, "e2e-search-skill")
}

// TestTUISettingsBackfillKeepsLogLevel 设置页回填当前值（P1.2 回归）：
// 当前 logLevel=debug 时打开设置页，下拉必须显示 debug；
// 只改端口保存不得把日志级别误写成 info。
func TestTUISettingsBackfillKeepsLogLevel(t *testing.T) {
	e := startE2EWith(t, Options{}, false)
	if err := e.a.b.client.PatchSettings(map[string]any{"logLevel": "debug"}); err != nil {
		t.Fatalf("预置 logLevel=debug 失败: %v", err)
	}

	e.key(tcell.KeyF8, 0)
	e.waitForText(t, "运行参数", 5*time.Second)

	// 回填断言：下拉当前值必须是 debug（修复前恒为 info）。
	// syncUI=false 下 load 异步完成，轮询等待回填落定。
	got := ""
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.a.ui(func() {
			idx, _ := e.settingsPage().levelDrop.GetCurrentOption()
			if idx >= 0 && idx < len(logLevels) {
				got = logLevels[idx]
			}
		})
		if got == "debug" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got != "debug" {
		t.Fatalf("日志级别下拉未回填当前值：got %q want debug", got)
	}

	// 不改任何值直接保存 → 不应产生字段写入
	e.focus(e.settingsPage().form.GetButton(0)) // 「保存运行参数」
	e.key(tcell.KeyEnter, 0)
	e.waitForText(t, "没有要保存", 5*time.Second) // 状态栏 120 列内会被截断，取短前缀
	st, err := e.a.b.client.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if st.LogLevel != "debug" {
		t.Fatalf("未改动也应保持 debug，实际 %q（修复前只改端口会被写成 info）", st.LogLevel)
	}

	// 只改端口保存 → 端口写入，logLevel 仍是 debug
	// （「设置已保存」flash 会被更长的端口提示覆盖 + 状态栏截断，不作断言；daemon 状态为准）
	e.a.ui(func() { e.settingsPage().portField.SetText("6123") })
	time.Sleep(100 * time.Millisecond)
	e.focus(e.settingsPage().form.GetButton(0))
	e.key(tcell.KeyEnter, 0)
	portDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(portDeadline) {
		st, err = e.a.b.client.Settings()
		if err == nil && st.Port == 6123 {
			if st.LogLevel != "debug" {
				t.Fatalf("只改端口后 logLevel 被误写为 %q", st.LogLevel)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("端口修改未生效")
}

// TestGroupClearMembersViaClient 分组成员清空（P1.3 回归）：PATCH 必须发显式空数组，
// daemon parseGroupInput 才会清空成员（null = 保留原成员）。
func TestGroupClearMembersViaClient(t *testing.T) {
	e := startE2EWith(t, Options{}, false)
	srv, err := e.a.b.client.CreateServer(map[string]any{
		"name": "clear-group-e2e", "transport": "stdio", "command": "npx",
	})
	if err != nil {
		t.Fatalf("创建 server 失败: %v", err)
	}
	if err := e.a.b.client.SaveGroup("", "g1", []string{srv.ID}, []string{store.AgentClaudeCode}); err != nil {
		t.Fatalf("创建分组失败: %v", err)
	}
	groups, err := e.a.b.client.Groups()
	if err != nil || len(groups) != 1 {
		t.Fatalf("分组读取异常: %v %+v", err, groups)
	}
	g := groups[0]
	if len(g.ServerIDs) != 1 || len(g.AgentTypes) != 1 {
		t.Fatalf("初始成员不符: %+v", g)
	}

	// 全不勾选保存（TUI saveGroup 传 nil → client 归一化为 []）→ 成员应清空
	if err := e.a.b.client.SaveGroup(g.ID, g.Name, nil, nil); err != nil {
		t.Fatalf("清空保存失败: %v", err)
	}
	groups, err = e.a.b.client.Groups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].ServerIDs) != 0 || len(groups[0].AgentTypes) != 0 {
		t.Fatalf("成员未被清空（修复前 PATCH null 保留原成员）: %+v", groups)
	}
}

func fmt_SkillMD(name string) string { return strings.ReplaceAll(skillMDTemplate, "%s", name) }

