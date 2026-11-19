package desktop

// 原生面板壳（跨平台调度）。
//
// 形态：窗口由 beaver.exe 自身创建（Windows 用 WebView2 宿主），不再拉起
// Edge/Chrome 的 --app 窗口，因此不再有浏览器进程参与；WebView2 Runtime 缺失
// 或非 Windows 平台仍回退原浏览器链路，保证可用性。
//
// 进程模型：窗口由分离式 `__panel` 子进程承载，一个进程一个窗口。窗口关闭 =
// 子进程退出，托盘 / CLI 立即返回，不会残留 WebView2 宿主或阻塞 daemon。

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PanelTitle 原生面板窗口标题。跨进程聚焦（FindWindow）按它匹配，改动需同步。
const PanelTitle = "Beaver（河狸）"

// PanelSpec 面板窗口参数。
type PanelSpec struct {
	BaseURL   string
	HomeDir   string
	Title     string
	Width     int
	Height    int
	MinWidth  int
	MinHeight int
}

func defaultPanelSpec(base, homeDir string) PanelSpec {
	return PanelSpec{
		BaseURL:   base,
		HomeDir:   homeDir,
		Title:     PanelTitle,
		Width:     1280,
		Height:    860,
		MinWidth:  1000,
		MinHeight: 640,
	}
}

// dataDir WebView2 用户数据目录：放在 beaver 数据目录下，不污染 exe 所在目录
// （go-webview2 默认会落到 %AppData%\<exe 名>）。
func (s PanelSpec) dataDir() string { return filepath.Join(s.HomeDir, "webview2") }

func (s PanelSpec) iconPath() string { return filepath.Join(s.dataDir(), "beaver.ico") }

// LaunchPanel 打开面板：已有原生窗口则聚焦；否则由分离式 `__panel` 子进程承载窗口。
// base 为空时先确保 daemon 在跑并探测实际端口。
func LaunchPanel(homeOverride string, portOverride *int, agentHomeOverride, base string) error {
	if focusPanelWindow() {
		return nil
	}
	if base == "" {
		b, err := EnsureRunning(homeOverride, portOverride, agentHomeOverride)
		if err != nil {
			return err
		}
		base = b
	}
	return launchPanelProcess(homeOverride, portOverride, agentHomeOverride, base)
}

// FocusPanel 把已有原生面板窗口带到前台；没有窗口时返回 false。
func FocusPanel() bool { return focusPanelWindow() }

// ClosePanel 关闭已有原生面板窗口（托盘退出时用，避免留下无主窗口进程）。
func ClosePanel() { closePanelWindow() }

// appendPanelLog 面板 / daemon 就绪链路的诊断日志，始终写入 <home>/logs/panel.log。
// 双击路径没有可见控制台，静默失败（白屏、托盘未起）只能靠这份日志定位，
// 因此不设 debug 开关；写入失败本身也不能影响主流程。
func appendPanelLog(homeDir, format string, args ...any) {
	if homeDir == "" {
		return
	}
	dir := filepath.Join(homeDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "panel.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
