//go:build windows

package desktop

// Windows 原生面板壳：WebView2 宿主（纯 Go，无 CGO，不依赖 WebView2Loader.dll 落盘）。
// go-webview2 内嵌 WebView2Loader 并按需从内存加载，因此单文件 exe 即可运行。
// 若系统没有 WebView2 Runtime（Win7 / 精简系统）则回退浏览器 --app 链路。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

const (
	imageIcon      = 1
	lrLoadFromFile = 0x00000010
	lrDefaultSize  = 0x00000040
	wmSetIcon      = 0x0080
	wmClose        = 0x0010
	iconSmall      = 0
	iconBig        = 1
	swShow         = 5
	swRestore      = 9
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW       = user32.NewProc("FindWindowW")
	procShowWindow        = user32.NewProc("ShowWindow")
	procIsIconic          = user32.NewProc("IsIconic")
	procSetForeground     = user32.NewProc("SetForegroundWindow")
	procSendMessageW      = user32.NewProc("SendMessageW")
	procPostMessageW      = user32.NewProc("PostMessageW")
	procLoadImageW        = user32.NewProc("LoadImageW")
	webviewRuntimeVersion struct {
		once    sync.Once
		version string
	}
)

// nativePanelAvailable 检测 WebView2 Runtime；缺失时保留浏览器回退。
// BEAVER_NO_NATIVE_PANEL=1 可强制走旧链路（排障用）。
func nativePanelAvailable() bool {
	if os.Getenv("BEAVER_NO_NATIVE_PANEL") == "1" {
		return false
	}
	webviewRuntimeVersion.once.Do(func() {
		if v, err := webviewloader.GetInstalledVersion(); err == nil {
			webviewRuntimeVersion.version = v
		}
	})
	return webviewRuntimeVersion.version != ""
}

// launchPanelProcess 以分离式 `__panel` 子进程承载窗口。
// 窗口进程与托盘 / CLI 解耦：窗口关闭即进程退出，下一次打开必然是全新宿主。
func launchPanelProcess(homeOverride string, portOverride *int, agentHomeOverride, base string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("解析自身可执行文件失败: %w", err)
	}
	cmd := exec.Command(exe, "__panel")
	if homeOverride != "" {
		cmd.Args = append(cmd.Args, "--home", homeOverride)
	}
	if portOverride != nil {
		cmd.Args = append(cmd.Args, "--port", fmt.Sprint(*portOverride))
	}
	if agentHomeOverride != "" {
		cmd.Args = append(cmd.Args, "--agent-home", agentHomeOverride)
	}
	// 托盘自持 daemon 时实际端口可能漂移（--port 覆盖 / 6280 被占），
	// 子进程不能只按配置端口推导，这里把父进程已验证的基地址直接传下去。
	cmd.Env = append(os.Environ(), "BEAVER_PANEL_URL="+base)
	// 不能设 HideWindow：Go 会写入 STARTF_USESHOWWINDOW=SW_HIDE，子进程窗口的
	// 第一次 ShowWindow 会被系统按启动信息覆盖为隐藏（面板窗口不出现）。
	// DETACHED_PROCESS 本身不创建控制台，黑窗问题不存在。
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动面板进程失败: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// ShowPanel 在调用进程内承载面板窗口（阻塞到窗口关闭）。
// `__panel` 子进程专用；原生壳不可用时回退浏览器并立即返回。
func ShowPanel(homeDir, base string) error {
	spec := defaultPanelSpec(base, homeDir)
	if !nativePanelAvailable() {
		return OpenAppWindow(base)
	}
	if err := runNativePanel(spec); err != nil {
		logPanelFailure(spec, err)
		return OpenAppWindow(base)
	}
	return nil
}

// runNativePanel 创建 WebView2 窗口并进入消息泵。
// 必须在锁定后的 OS 线程上执行：WebView2 要求窗口与消息泵同线程。
func runNativePanel(spec PanelSpec) (err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("原生面板异常: %v", r)
		}
	}()

	if err := os.MkdirAll(spec.dataDir(), 0o755); err != nil {
		return err
	}
	appendPanelLog(spec, "开始创建原生窗口，WebView2 运行时 %s", webviewRuntimeVersion.version)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("BEAVER_PANEL_DEBUG") == "1",
		DataPath:  spec.dataDir(),
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  spec.Title,
			Width:  uint(spec.Width),
			Height: uint(spec.Height),
			Center: true,
		},
	})
	if w == nil {
		return fmt.Errorf("WebView2 窗口初始化失败（运行时 %s）", webviewRuntimeVersion.version)
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	appendPanelLog(spec, "窗口已创建 hwnd=%d，导航到 %s", hwnd, spec.BaseURL)
	// 兜底：即便启动信息带了 SW_HIDE，初始化完成后也显式显示窗口。
	_, _, _ = procShowWindow.Call(hwnd, swShow)

	applyPanelIcon(hwnd, spec)
	if spec.MinWidth > 0 && spec.MinHeight > 0 {
		w.SetSize(spec.MinWidth, spec.MinHeight, webview2.HintMin)
	}
	w.SetTitle(spec.Title)
	w.Navigate(spec.BaseURL)
	w.Run()
	appendPanelLog(spec, "消息泵已退出（窗口关闭）")
	return nil
}

// applyPanelIcon 把品牌托盘图标设为窗口/任务栏图标（默认会是系统通用图标）。
func applyPanelIcon(hwnd uintptr, spec PanelSpec) {
	if hwnd == 0 {
		return
	}
	if err := os.WriteFile(spec.iconPath(), TrayIconICO(), 0o644); err != nil {
		return
	}
	path, err := windows.UTF16PtrFromString(spec.iconPath())
	if err != nil {
		return
	}
	h, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(path)), imageIcon, 0, 0, lrLoadFromFile|lrDefaultSize)
	if h == 0 {
		return
	}
	_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, iconBig, h)
	_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, h)
}

// findPanelWindow 跨进程查找面板窗口：先按库固定的窗口类 + 标题匹配，
// 再退回只按标题匹配（页面标题可能被 WebView2 覆盖为文档标题）。
func findPanelWindow() uintptr {
	title, err := windows.UTF16PtrFromString(PanelTitle)
	if err != nil {
		return 0
	}
	class, _ := windows.UTF16PtrFromString("webview")
	if hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title))); hwnd != 0 {
		return hwnd
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	return hwnd
}

func focusPanelWindow() bool {
	hwnd := findPanelWindow()
	if hwnd == 0 {
		return false
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		_, _, _ = procShowWindow.Call(hwnd, swRestore)
	} else {
		_, _, _ = procShowWindow.Call(hwnd, swShow)
	}
	_, _, _ = procSetForeground.Call(hwnd)
	return true
}

func closePanelWindow() {
	if hwnd := findPanelWindow(); hwnd != 0 {
		_, _, _ = procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}
}

func logPanelFailure(spec PanelSpec, err error) {
	appendPanelLog(spec, "原生面板不可用，回退浏览器窗口: %v", err)
}

// appendPanelLog 面板窗口诊断日志。阶段日志仅在 BEAVER_PANEL_DEBUG=1 时写入，
// 「原生面板不可用」的失败记录始终写入。
func appendPanelLog(spec PanelSpec, format string, args ...any) {
	if os.Getenv("BEAVER_PANEL_DEBUG") != "1" && !strings.Contains(format, "原生面板不可用") {
		return
	}
	dir := filepath.Join(spec.HomeDir, "logs")
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(filepath.Join(dir, "panel.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if openErr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
