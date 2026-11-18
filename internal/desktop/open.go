// Package desktop 桌面壳：面板窗口打开链与 daemon 就绪保障。
// 对应 docs/desktop-plan.md §4 M2 决策：
//   - 窗口优先 Edge --app（无地址栏独立窗口，观感同原生）→ Chrome --app → 默认浏览器兜底；
//   - Windows 经 ShellExecute 语义（cmd /c start）启动，不直接 spawn（不解析 App Paths）；
//   - 加 --profile-directory=Default：多 profile 用户否则被 Edge 选择器卡住；
//   - open 语义 = 确保 daemon 在跑（未跑则拉起并等待就绪）再开窗；
//   - Firefox 无 --app，自然落入默认浏览器兜底。
package desktop

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hxgh776/beaver/internal/autostart"
	"github.com/hxgh776/beaver/internal/daemon"
	"github.com/hxgh776/beaver/internal/store"
)

// EnsureRunning 确保 daemon 在跑：未跑则分离式拉起并等待 API 就绪。
// portOverride / agentHomeOverride 原样传给子进程，避免桌面入口丢失
// `--port` / `--agent-home` 重定向（VENU-16 复审修复）。
// 返回 daemon 实际可达的基地址。
func EnsureRunning(homeOverride string, portOverride *int, agentHomeOverride string) (string, error) {
	s := store.NewStore(homeOverride)
	config, err := s.Load()
	if err != nil {
		return "", err
	}
	pidPath := s.PidPath()
	if pid, _ := daemon.ReadPid(pidPath); pid > 0 && daemon.IsPidAlive(pid) {
		return baseURL(config), nil
	}
	if _, err := daemon.SpawnDetached(homeOverride, portOverride, agentHomeOverride); err != nil {
		return "", err
	}
	// 显式端口不持久化、端口冲突还会向后漂移，因此按候选区间探测实际监听端口。
	client := &http.Client{Timeout: 1 * time.Second}
	token := config.Settings.Token
	desired := config.Settings.Port
	if portOverride != nil {
		desired = *portOverride
	}
	for i := 0; i < 50; i++ {
		for offset := 0; offset <= 10; offset++ {
			base := fmt.Sprintf("http://127.0.0.1:%d", desired+offset)
			req, _ := http.NewRequest("GET", base+"/api/status", nil)
			req.Header.Set("authorization", "Bearer "+token)
			if res, err := client.Do(req); err == nil {
				_ = res.Body.Close()
				if res.StatusCode == http.StatusOK {
					return base, nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	logPath := filepath.Join(s.LogsDir(), "daemon.log")
	return "", fmt.Errorf("daemon 启动超时，请查看日志: %s", logPath)
}

func baseURL(config *store.Config) string {
	return fmt.Sprintf("http://127.0.0.1:%d", config.Settings.Port)
}

// OpenPanel 确保 daemon 在跑后，以最高保真形态打开面板窗口。
// homeOverride 为空 = 默认数据目录。
func OpenPanel(homeOverride string, portOverride *int, agentHomeOverride string) error {
	base, err := EnsureRunning(homeOverride, portOverride, agentHomeOverride)
	if err != nil {
		return err
	}
	return OpenAppWindow(base)
}

// OpenAppWindow 按平台最优链打开独立应用窗口（Edge --app → Chrome --app → 默认浏览器）。
func OpenAppWindow(base string) error {
	switch runtime.GOOS {
	case "windows":
		return openWindows(base)
	case "darwin":
		return openMacos(base)
	default:
		return openLinux(base)
	}
}

// OpenPath 用系统文件管理器打开目录/文件（设置页「打开数据目录」）。
func OpenPath(path string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

// —— Windows：App Paths 注册表 / 已知安装路径探测 + cmd start 启动（ShellExecute 语义） ——//

func openWindows(base string) error {
	for _, name := range []string{"msedge.exe", "chrome.exe"} {
		exe := browserExe(name)
		if exe == "" {
			continue
		}
		// 每个开关必须是独立参数：拼成一个字符串会被浏览器当成单个非法开关，
		// --app 窗口退化成普通标签页甚至打不开（VENU-16 复审修复）。
		c := exec.Command("cmd", "/c", "start", "", exe, "--app="+base, "--profile-directory=Default")
		autostart.HideWindow(c)
		if err := c.Start(); err == nil {
			go func() { _ = c.Wait() }()
			return nil
		}
	}
	// Edge 在 Win10/11 必在——到这里说明环境异常，兜底默认浏览器
	return openDefault(base)
}

// browserExe 先查 App Paths 注册表，再退回常见安装目录（Edge 必在，Chrome per-user 很常见）。
func browserExe(name string) string {
	if exe := appPathsExe(name); exe != "" {
		return exe
	}
	return knownInstallPath(name)
}

func knownInstallPath(name string) string {
	dirs := []string{}
	switch name {
	case "msedge.exe":
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
			if root := os.Getenv(env); root != "" {
				dirs = append(dirs, filepath.Join(root, "Microsoft", "Edge", "Application"))
			}
		}
	case "chrome.exe":
		for _, env := range []string{"LOCALAPPDATA", "ProgramFiles", "ProgramFiles(x86)"} {
			if root := os.Getenv(env); root != "" {
				dirs = append(dirs, filepath.Join(root, "Google", "Chrome", "Application"))
			}
		}
	}
	for _, dir := range dirs {
		exe := filepath.Join(dir, name)
		if _, err := os.Stat(exe); err == nil {
			return exe
		}
	}
	return ""
}

// appPathsExe 查 App Paths 注册表（HKLM + HKCU，Chrome per-user 安装在 %LOCALAPPDATA% 很常见）。
func appPathsExe(name string) string {
	for _, root := range []string{`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`, `HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`} {
		out, err := exec.Command("reg", "query", root+name, "/ve").CombinedOutput()
		if err != nil {
			continue
		}
		if exe := parseAppPathOutput(string(out)); exe != "" {
			return exe
		}
	}
	return ""
}

// parseAppPathOutput 从 reg query 输出中取 REG_SZ 后的可执行文件路径。
// 注意：reg 输出带缩进且 "(默认)" 可能被本地代码页转成乱码，因此不能依赖行首空格判断。
func parseAppPathOutput(out string) string {
	for _, line := range strings.Split(out, "\n") {
		idx := strings.Index(line, "REG_SZ")
		if idx < 0 {
			continue
		}
		exe := strings.TrimSpace(line[idx+len("REG_SZ"):])
		if exe == "" {
			continue
		}
		if _, err := os.Stat(exe); err == nil {
			return exe
		}
	}
	return ""
}

func openDefault(url string) error {
	c := exec.Command("cmd", "/c", "start", "", url)
	autostart.HideWindow(c)
	return c.Start()
}

// —— macOS：/Applications 下探测 Chromium 变体；Firefox 无 --app 自然兜底 ——//

func openMacos(base string) error {
	candidates := []string{
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
	}
	target := "--app=" + base
	for _, exe := range candidates {
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		if err := exec.Command(exe, target, "--profile-directory=Default").Start(); err == nil {
			return nil
		}
	}
	return exec.Command("open", base).Start()
}

// —— Linux：PATH 下的 chromium 变体 ——//

func openLinux(base string) error {
	candidates := []string{"microsoft-edge", "google-chrome", "chromium", "chromium-browser", "brave"}
	target := "--app=" + base
	for _, name := range candidates {
		if _, err := exec.LookPath(name); err != nil {
			continue
		}
		if err := exec.Command(name, target, "--profile-directory=Default").Start(); err == nil {
			return nil
		}
	}
	return exec.Command("xdg-open", base).Start()
}

var _ = context.Background
