package main

// beaver desktop / 无参数启动：双击 exe 即打开桌面面板窗口（docs/desktop-plan.md §4 M2）。
// 形态 = 托盘常驻 + 独立应用窗口（Windows 用 WebView2 原生壳，不依赖浏览器进程；
// WebView2 不可用时回退 Edge/Chrome --app），不再进入终端界面；终端面板仍可用
// `beaver tui` 显式进入。
// 双击与终端启动的区别：双击时 Windows 会为本进程新建独占控制台，
// 这里先把它隐藏，避免黑窗一闪；终端内启动保留输出。

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hxgh776/beaver/internal/daemon"
	"github.com/hxgh776/beaver/internal/desktop"
	"github.com/hxgh776/beaver/internal/store"
)

// trayPidPath 托盘壳 pid 文件（与 daemon pid 分开：托盘退出不等于 daemon 退出）。
func trayPidPath() string {
	s := store.NewStore(homeOverride)
	return filepath.Join(s.HomeDir, "tray.pid")
}

// trayAlive 检测已有托盘壳在跑；双击第二次只开窗口，不重复常驻。
// pid 文件可能残留，且该 pid 可能已被系统复用给无关进程，因此还要校验
// 进程镜像就是本 exe——否则会把「陈旧 pid」误判成托盘仍在，跳过拉起 daemon，
// 窗口导航到无监听的端口（白屏且无任何错误）。
func trayAlive() bool {
	s := store.NewStore(homeOverride)
	data, err := os.ReadFile(filepath.Join(s.HomeDir, "tray.pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(string(trimSpaceBytes(data)))
	if err != nil || pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return false
	}
	if daemon.IsPidAlive(pid) && daemon.IsSameExecutable(pid) {
		return true
	}
	appendDesktopLog(s.HomeDir, "忽略陈旧 tray.pid：pid=%d 已退出或已被无关进程复用", pid)
	return false
}

// appendDesktopLog 把托盘 / 面板启动链路的诊断写进 <home>/logs/panel.log。
// 双击路径没有控制台，静默失败只能靠这份日志定位。
func appendDesktopLog(homeDir, format string, args ...any) {
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

func trimSpaceBytes(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	for len(b) > 0 && (b[0] == '\n' || b[0] == '\r' || b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	return b
}

func cmdDesktop() error {
	// 双击新建的独占控制台：隐藏，避免终端界面抢在窗口前出现。
	if ownsConsole() {
		hideConsoleWindow()
	}

	// 已有托盘壳 = 单实例：只把面板窗口带到前台（没有窗口则新起一个窗口进程）。
	if trayAlive() {
		base, err := desktop.EnsureRunning(homeOverride, portOverride, agentHomeOverride)
		if err != nil {
			desktopNotify("Beaver 启动失败", err.Error())
			return err
		}
		return desktop.LaunchPanel(homeOverride, portOverride, agentHomeOverride, base)
	}

	return cmdTrayShell(true)
}

func desktopFailure(err error) {
	fmt.Fprintln(os.Stderr, "✗", err.Error())
	desktopNotify("Beaver 启动失败", err.Error())
}
