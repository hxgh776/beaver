package tui

// 面板外壳：侧栏页签 + 内容区 + 状态栏。
// 页面切换 F1-F8 / Alt+1..8 / 侧栏回车；Ctrl+P 聚焦侧栏；Ctrl+Q 退出（自持有 daemon 时确认）。

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"

	"github.com/hxgh776/beaver/internal/daemon"
	"github.com/hxgh776/beaver/internal/gateway"
	"github.com/hxgh776/beaver/internal/store"
	"github.com/hxgh776/beaver/internal/version"
)

// Options TUI 启动选项（CLI --home/--agent-home/--port 透传）。
type Options struct {
	HomeDir   string
	AgentHome string
	Port      *int
	// Snapshot 非空 = 无头快照模式：渲染指定页面（key 或 all）后把屏幕文本打到 stdout 退出。
	// 用于验收证明与 CI——与交互模式走同一渲染路径。
	Snapshot string
	// SyncUI true = 页面数据加载与调用方同步（快照/测试模式用，保证断言确定性）。
	SyncUI bool
	// Screen 测试/快照注入的自定义屏幕（nil = 交互模式自行创建）。
	Screen tcell.Screen
	// Width/Height 快照/测试模式的屏幕尺寸（默认 120x40）。
	Width, Height int
	// Capture 非 nil 时，每页渲染结果（屏幕文本行）追加到该切片（屏幕 Fini 前捕获）。
	Capture *[][]string
	// SkillsShBase 非空 = 覆盖 skills.sh 搜索 API 基地址（e2e 测试桩注入；生产为空）。
	SkillsShBase string
	// SkillsFetchZip 非空 = 覆盖技能仓库 ZIP 下载实现（e2e 测试注入内存 ZIP；生产为 nil）。
	SkillsFetchZip func(ctx context.Context, url string) ([]byte, error)
}

// booted daemon 启动结果（自持有 or 远端挂载）。
type booted struct {
	client    *Client
	ownDaemon bool
	d         *daemon.Daemon
	gw        *gateway.Gateway
	base      string
	homeDir   string
	config    *store.Config
}

// boot 与 tray 同款双模式判定：pid 文件存活 = 远端挂载；否则本进程自持有 daemon + 网关钩子。
func boot(opts Options) (*booted, error) {
	s := store.NewStore(opts.HomeDir)
	config, err := s.Load()
	if err != nil {
		return nil, err
	}
	pid, _ := daemon.ReadPid(s.PidPath())
	remote := pid > 0 && daemon.IsPidAlive(pid)

	var d *daemon.Daemon
	var gw *gateway.Gateway
	if !remote {
		d = daemon.New(daemon.Options{
			HomeDir:         opts.HomeDir,
			AgentConfigRoot: opts.AgentHome,
			Port:            opts.Port,
			SkillsShBase:    opts.SkillsShBase,
			SkillsFetchZip:  opts.SkillsFetchZip,
		})
		gw = gateway.New(d)
		gw.Attach()
		if err := d.Start(); err != nil {
			return nil, fmt.Errorf("daemon 启动失败（端口 %d 可能被占用）: %w", config.Settings.Port, err)
		}
		if err := os.WriteFile(daemon.PidFilePath(d.Store.HomeDir), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			return nil, fmt.Errorf("写入 PID 文件失败: %w", err)
		}
	}

	b := &booted{
		ownDaemon: !remote,
		d:         d,
		gw:        gw,
		homeDir:   s.HomeDir,
		config:    config,
	}
	if remote {
		b.base = fmt.Sprintf("http://127.0.0.1:%d", config.Settings.Port)
	} else {
		b.base = d.BaseURL()
	}
	b.client = NewClient(b.base, config.Settings.Token)
	return b, nil
}

// shutdown 自持有的 daemon 随面板退出（与 tray 退出语义一致）；远端模式保留后台运行。
func (b *booted) shutdown() {
	if !b.ownDaemon || b.d == nil {
		return
	}
	_ = b.gw.Detach()
	_ = b.d.Stop()
	_ = os.Remove(daemon.PidFilePath(b.d.Store.HomeDir))
}

// pageDef 单个页面：key（F 键序号）、标题、描述、根原语、主焦点件与同步 load。
type pageDef struct {
	key   string
	title string
	desc  string
	prim  tview.Primitive
	// focus 切页/关模态时的焦点目标（页面的主交互件——表格/输入框/首个按钮）。
	// tview 的 Flex 不做 Tab 轮转，焦点必须显式落在可交互原语上。
	focus tview.Primitive
	load  func(a *App)
	// inst 页面结构体实例（测试经 pageStruct 取用，驱动页面级部件）。
	inst any
}

// pageStruct 按页 key 返回页面结构体实例（类型断言由调用方完成；仅测试用）。
func (a *App) pageStruct(key string) any {
	for _, def := range a.pageDefs {
		if def.key == key {
			return def.inst
		}
	}
	return nil
}

// App TUI 应用状态。
// 线程约定：
//   - 事件回调（按钮/按键/回车/表格选中）运行在事件循环 goroutine：只做部件直接操作，
//     或经 a.post() 异步调度——严禁调用 a.ui()（QueueUpdate 同步等待自身 → 死锁）；
//   - 后台 goroutine 内的部件操作经 a.ui() 排队（同步等待完成）；
//   - a.post() 两种上下文都安全（推荐默认），a.ui() 只允许非事件循环 goroutine 调用。
type App struct {
	b      *booted
	tapp   *tview.Application
	root   tview.Primitive
	pages  *tview.Pages
	sidebar *tview.List
	statusbar *tview.TextView
	headline  *tview.TextView
	pageDefs []*pageDef
	current  int
	syncUI   bool
	loadGen  int64
	genMu    sync.Mutex
	flashMu  sync.Mutex
	flashMsg string
	flashUntil time.Time
}

func newApp(b *booted, syncUI bool) *App {
	return &App{
		b:      b,
		tapp:   tview.NewApplication(),
		syncUI: syncUI,
	}
}

// Run 启动面板：确保 daemon（自持有或远端）→ 构建 UI → 交互循环或无头快照。
// 两种模式共用真实事件循环：无头模式在模拟屏幕上跑，UI 变更一律经 QueueUpdateDraw
// 序列化到循环（tview 的 Draw 在循环未运行时会阻塞等待，直接调用会死锁）。
func Run(opts Options) error {
	b, err := boot(opts)
	if err != nil {
		return err
	}
	a := newApp(b, opts.SyncUI)
	a.buildLayout()

	if opts.Snapshot != "" || opts.Screen != nil {
		return a.runHeadless(opts)
	}

	// 交互模式：周期刷新当前页（网关/日志类页面依赖轮询）
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				a.refreshCurrent()
			case <-done:
				return
			}
		}
	}()
	defer b.shutdown()
	go a.selectPage(0) // 首屏加载（等事件循环起来后生效）
	if err := a.tapp.Run(); err != nil {
		return err
	}
	return nil
}

// runHeadless 无头渲染：模拟屏幕 + 真实事件循环，走与交互模式相同的渲染路径。
// 快照模式下把屏幕文本写 stdout 后退出；测试注入 Screen 并断言渲染结果。
func (a *App) runHeadless(opts Options) error {
	defer a.b.shutdown() // 自持有的 daemon 同样随快照退出
	screen := opts.Screen
	if screen == nil {
		screen = tcell.NewSimulationScreen("UTF-8")
	}
	w, h := opts.Width, opts.Height
	if w <= 0 {
		w = 120
	}
	if h <= 0 {
		h = 40
	}
	if sim, ok := screen.(tcell.SimulationScreen); ok {
		sim.SetSize(w, h)
	}
	// SetScreen 内部会调用 screen.Init()（勿手动 Init，双重初始化会破坏模拟屏幕）
	a.tapp.SetScreen(screen)
	a.tapp.SetRoot(a.root, true)

	loopErr := make(chan error, 1)
	go func() { loopErr <- a.tapp.Run() }()

	// 等待事件循环就绪（首次 ui 往返）；超时说明 Run 启动失败
	synced := make(chan struct{})
	go func() { a.ui(func() {}); close(synced) }()
	select {
	case <-synced:
	case <-time.After(10 * time.Second):
		a.tapp.Stop()
		return fmt.Errorf("TUI 事件循环未能启动")
	}

	if os.Getenv("BEAVER_TUI_DEBUG") != "" {
		first := ScreenText(screen, w, h)
		nonBlank := 0
		for _, l := range first {
			if strings.TrimSpace(l) != "" {
				nonBlank++
			}
		}
		fmt.Fprintf(os.Stderr, "[tui-debug] 首屏（sync 后）非空行=%d/%d 首行=%q\n", nonBlank, h, first[0])
	}

	// selectPage 经 post 异步调度切页；快照需要确定性渲染——ui 空操作作栅栏，
	// 等切页（及 syncUI 模式下随后的同步数据加载）全部应用完成后再 dump 屏幕
	switchPage := func(i int) {
		a.selectPage(i)
		a.ui(func() {})
	}
	target := opts.Snapshot
	if target == "" || target == "all" {
		for i := range a.pageDefs {
			switchPage(i)
			a.dumpScreen(screen, w, h, true)
			a.capture(opts, screen, w, h)
		}
	} else {
		idx := -1
		for i, def := range a.pageDefs {
			if def.key == target {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("未知页面: %s（可选: %s/all）", target, strings.Join(pageKeys(a.pageDefs), "/"))
		}
		switchPage(idx)
		a.dumpScreen(screen, w, h, true)
		a.capture(opts, screen, w, h)
	}
	a.tapp.Stop()
	return <-loopErr
}

// capture 捕获当前屏幕文本（Fini 前调用才有效）。
func (a *App) capture(opts Options, screen tcell.Screen, w, h int) {
	if opts.Capture != nil {
		*opts.Capture = append(*opts.Capture, ScreenText(screen, w, h))
	}
}

func pageKeys(defs []*pageDef) []string {
	keys := make([]string, 0, len(defs))
	for _, def := range defs {
		keys = append(keys, def.key)
	}
	return keys
}

// dumpScreen 从屏幕缓冲还原文本（快照模式 stdout / 测试断言共用）。
func (a *App) dumpScreen(screen tcell.Screen, w, h int, toStdout bool) {
	lines := ScreenText(screen, w, h)
	if toStdout {
		fmt.Println(strings.Join(lines, "\n"))
	}
}

// ScreenText 读取屏幕单元格拼成文本行（快照输出/测试断言共用）。
// 宽字符（CJK）的填充单元格剔除，避免输出出现"概 览"式空隙。
func ScreenText(screen tcell.Screen, w, h int) []string {
	lines := make([]string, 0, h)
	for y := 0; y < h; y++ {
		var sb strings.Builder
		prevWide := false
		for x := 0; x < w; x++ {
			r, _, _, _ := screen.GetContent(x, y)
			if r == 0 {
				r = ' '
			}
			if r == ' ' && prevWide { // 宽字符第二格的填充空格
				prevWide = false
				continue
			}
			sb.WriteRune(r)
			prevWide = runewidth.RuneWidth(r) >= 2
		}
		lines = append(lines, strings.TrimRight(sb.String(), " "))
	}
	return lines
}

// —— 布局 —— //

var pageTitles = []struct {
	key, title, desc string
}{
	{"status", "概览", "守护进程状态、运行环境与本机 Agent"},
	{"servers", "MCP 管理", "所有 MCP server 的统一管理：注册、启停与工具开关"},
	{"discovery", "MCP 发现", "浏览官方 / Smithery / npm 等九个源，一键收录"},
	{"sync", "Agent 分发", "分组管理，一次配置同步到 Claude Code、Cursor 等 agent"},
	{"gateway", "聚合网关", "多个 MCP 聚合为一个端点、访问令牌与上游进程"},
	{"logs", "调用日志", "网关调用记录与上游进程输出"},
	{"skills", "Skill 管理", "安装 / 启停 / 更新 SKILL.md 技能，分发到各 agent"},
	{"settings", "设置", "端口、开机自启、恢复备份、数据目录与关于"},
}

func (a *App) buildLayout() {
	a.pages = tview.NewPages()
	builders := []func(a *App) *pageDef{
		newStatusPage, newServersPage, newDiscoveryPage, newSyncPage,
		newGatewayPage, newLogsPage, newSkillsPage, newSettingsPage,
	}
	for i, mk := range builders {
		def := mk(a)
		def.key = pageTitles[i].key
		def.title = pageTitles[i].title
		def.desc = pageTitles[i].desc
		a.pageDefs = append(a.pageDefs, def)
		a.pages.AddPage(def.key, def.prim, true, i == 0)
	}

	a.sidebar = tview.NewList()
	for i, def := range a.pageDefs {
		a.sidebar.AddItem(def.title, def.desc, rune('1'+i), func() {
			a.selectPage(i)
		})
	}
	a.sidebar.SetBorder(true).SetTitle(" Beaver 面板 ").SetTitleAlign(tview.AlignLeft)
	a.sidebar.SetMainTextColor(tcell.ColorDefault).
		SetSecondaryTextColor(tcell.ColorGray).
		SetSelectedTextColor(tcell.ColorBlack).
		SetSelectedBackgroundColor(tcell.ColorTeal)

	a.headline = tview.NewTextView().SetDynamicColors(true)
	a.statusbar = tview.NewTextView().SetDynamicColors(true)

	main := tview.NewFlex().
		AddItem(a.sidebar, 0, 1, false).
		AddItem(a.pages, 0, 4, true)
	a.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.headline, 1, 0, false).
		AddItem(main, 0, 1, true).
		AddItem(a.statusbar, 1, 0, false)

	a.tapp.SetRoot(a.root, true)
	a.tapp.SetInputCapture(a.globalKeys)
	a.tapp.EnableMouse(true)
	a.refreshStatusbar()
}

// globalKeys 全局按键：F1-F8 / Alt+1..8 切页，Ctrl+P 侧栏，Ctrl+Q 退出，Esc 关模态。
// 事件循环 goroutine 内不做阻塞调用：切页/确认框经 post 异步调度（selectPage 自身非阻塞）。
func (a *App) globalKeys(ev *tcell.EventKey) *tcell.EventKey {
	switch ev.Key() {
	case tcell.KeyF1, tcell.KeyF2, tcell.KeyF3, tcell.KeyF4, tcell.KeyF5, tcell.KeyF6, tcell.KeyF7, tcell.KeyF8:
		n := int(ev.Key()) - int(tcell.KeyF1)
		go a.selectPage(n)
		return nil
	case tcell.KeyCtrlQ:
		a.confirmQuit()
		return nil
	case tcell.KeyCtrlP:
		a.tapp.SetFocus(a.sidebar)
		return nil
	case tcell.KeyEscape:
		if a.pages.HasPage("__modal") {
			a.closeModal()
			return nil
		}
	case tcell.KeyRune:
		if ev.Modifiers()&tcell.ModAlt != 0 {
			if n := int(ev.Rune()) - '1'; n >= 0 && n < len(a.pageDefs) {
				go a.selectPage(n)
				return nil
			}
		}
	}
	return ev
}

// selectPage 切页。任意上下文可调用（侧栏回车/F 键/首屏加载/测试 goroutine）：
// 部件操作经 post 异步调度（事件循环内调用不阻塞），随后按模式加载页面数据。
func (a *App) selectPage(i int) {
	if i < 0 || i >= len(a.pageDefs) {
		return
	}
	def := a.pageDefs[i]
	a.post(func() {
		if a.hasModal() {
			a.closeModal()
		}
		a.current = i
		a.pages.SwitchToPage(def.key)
		if def.focus != nil {
			a.tapp.SetFocus(def.focus)
		} else {
			a.tapp.SetFocus(def.prim)
		}
		a.refreshStatusbar()
	})
	a.refreshPage(i)
}

// refreshCurrent 拉取当前页数据并刷新。
// 同步模式（快照/测试）在调用方 goroutine 直接拉取（内部 ui 调用仍排队到循环，
// 返回即已渲染）——测试因此可确定性断言；交互模式后台拉取。
// 注意：syncUI 模式的调用方必须是非事件循环 goroutine（快照/测试约定）。
func (a *App) refreshCurrent() {
	a.refreshPage(a.current)
}

func (a *App) refreshPage(i int) {
	if i < 0 || i >= len(a.pageDefs) {
		return
	}
	def := a.pageDefs[i]
	a.genMu.Lock()
	a.loadGen++
	gen := a.loadGen
	a.genMu.Unlock()
	if a.syncUI {
		def.load(a)
		return
	}
	go func() {
		def.load(a)
		a.ui(func() {
			// 过期加载结果丢弃（用户已切走或发起新刷新）
			a.genMu.Lock()
			stale := gen != a.loadGen
			a.genMu.Unlock()
			if stale {
				return
			}
			a.refreshStatusbar()
		})
	}()
}

// ui 把 UI 变更排队到事件循环并等待其完成（含重绘）。
// tview 的 QueueUpdate 是同步等待（updates 通道投递后 <-ch），事件循环内调用即等待
// 自身 → 死锁。因此本方法只允许非事件循环 goroutine 调用（页面数据加载、后台任务）；
// 事件回调内一律直接操作部件或走 post。
func (a *App) ui(fn func()) {
	_ = a.tapp.QueueUpdateDraw(fn)
}

// post 把 UI 变更异步调度到事件循环：任意 goroutine 可调用，事件循环内调用也安全——
// QueueUpdateDraw 的阻塞等待由独立 goroutine 承接，调用方立即返回，
// 循环回到 select 后即处理（tview 在每次事件处理后重绘，post 的变更必然被渲染）。
// 事件回调（侧栏回车/Ctrl+Q/模态按钮等）内统一走本方法或部件直接操作。
func (a *App) post(fn func()) { go a.ui(fn) }

// —— 模态 —— //

// showModal 覆盖式模态（命名页 __modal，Esc 关闭）。
// 调用方必须在事件循环 goroutine 内（按钮回调）或经 ui() 排队。
func (a *App) showModal(p tview.Primitive) {
	if a.pages.HasPage("__modal") {
		a.pages.RemovePage("__modal")
	}
	a.pages.AddPage("__modal", p, true, true)
	a.pages.SwitchToPage("__modal")
	// 焦点交给模态里第一个可交互原语（外层是 Flex/Box 包装，直接聚焦等于失焦）
	if inner := firstFocusable(p); inner != nil {
		a.tapp.SetFocus(inner)
	}
}

// firstFocusable 深度优先找第一个可交互原语（跳过纯容器与只读 TextView）。
func firstFocusable(p tview.Primitive) tview.Primitive {
	switch v := p.(type) {
	case *tview.Flex:
		for i := 0; i < v.GetItemCount(); i++ {
			if inner := firstFocusable(v.GetItem(i)); inner != nil {
				return inner
			}
		}
		return nil
	case *tview.Pages:
		for i := 0; i < v.GetPageCount(); i++ {
			page := v.GetPage(v.GetPageNames(false)[i])
			if inner := firstFocusable(page); inner != nil {
				return inner
			}
		}
		return nil
	case *tview.TextView:
		return nil
	default:
		return p
	}
}

func (a *App) closeModal() {
	if !a.pages.HasPage("__modal") {
		return
	}
	a.pages.RemovePage("__modal")
	def := a.pageDefs[a.current]
	a.pages.SwitchToPage(def.key)
	if def.focus != nil {
		a.tapp.SetFocus(def.focus)
	} else {
		a.tapp.SetFocus(def.prim)
	}
}

func (a *App) hasModal() bool { return a.pages != nil && a.pages.HasPage("__modal") }

// closeModalSafe 任意上下文可用的关模态（post 异步调度；事件循环内也可直接 closeModal）。
func (a *App) closeModalSafe() { a.post(a.closeModal) }

// confirm 通用确认框。post 异步调度（任意上下文可调用，事件循环内不阻塞）；
// onYes 在独立 goroutine 执行（可做阻塞 client 调用）。
func (a *App) confirm(title, body string, onYes func()) {
	a.post(func() {
		modal := tview.NewModal().
			SetText(title + "\n\n" + body).
			AddButtons([]string{"确认", "取消"}).
			SetDoneFunc(func(idx int, label string) {
				a.closeModal()
				if idx == 0 {
					go onYes()
				}
			})
		a.showModal(modal)
	})
}

// centered 把原语按给定尺寸居中（模态容器用）。
func centered(p tview.Primitive, w, h int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().
			AddItem(nil, 0, 1, false).
			AddItem(p, w, 0, true).
			AddItem(nil, 0, 1, false), h, 0, true).
		AddItem(nil, 0, 1, false)
}

// —— 状态栏 / 提示 —— //

func (a *App) refreshStatusbar() {
	mode := "远端挂载"
	if a.b.ownDaemon {
		mode = "本进程持有"
	}
	// 状态栏务必保持在一行宽度内（120 列）：提示 + daemon 模式 + 操作反馈
	hint := "[gray]F1-F8 页面 · Ctrl+P 侧栏 · Ctrl+Q 退出[-]"
	info := fmt.Sprintf("[gray]daemon:%s · v%s · %s[-]", mode, version.String(), a.flashText())
	a.statusbar.SetText(hint + "  " + info)
	a.headline.SetText(fmt.Sprintf("[black:teal:b] Beaver（河狸）— 本地 Agent 能力总控台 [white:teal]%s [-:-:b][gray]  %s[-]",
		a.pageDefs[a.current].title, a.b.base))
}

// flashText 临时消息（info/err），超时回落为空。
func (a *App) flashText() string {
	a.flashMu.Lock()
	defer a.flashMu.Unlock()
	if time.Now().Before(a.flashUntil) {
		return a.flashMsg
	}
	return ""
}

func (a *App) flash(msg, color string) {
	a.flashMu.Lock()
	a.flashMsg = "[" + color + "]" + msg + "[-]"
	a.flashUntil = time.Now().Add(8 * time.Second)
	a.flashMu.Unlock()
	// post 而非 ui：info/errd 会在事件回调内被直接调用，也可能嵌套在 ui 闭包里——
	// 两条路径都不允许同步等待事件循环
	a.post(func() { a.refreshStatusbar() })
}

// info 操作成功提示。
func (a *App) info(msg string) { a.flash(msg, "green") }

// errd 操作失败提示。
func (a *App) errd(err error) { a.flash("✗ " + err.Error(), "red") }

func (a *App) confirmQuit() {
	body := "daemon 由其他进程持有，将保留后台运行。"
	if a.b.ownDaemon {
		body = "面板正持有 daemon（本进程内启动），退出会一并停止 daemon 与网关。\n如需后台常驻，请先执行 beaver start 再打开面板。"
	}
	a.confirm("退出 Beaver 面板？", body, func() {
		a.tapp.Stop()
	})
}
