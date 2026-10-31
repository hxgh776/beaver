package main

// 开机自启：登录时静默拉起 `beaver start`（start 本身幂等，已在运行则直接返回）。
// Windows：HKCU Run 键 + wscript 隐藏窗口包装（用户级，免管理员）；
// macOS：~/Library/LaunchAgents 的 LaunchAgent（RunAtLoad）；
// Linux：systemd user unit（default.target）。
// 自启条目固定指向当前 CLI 可执行文件的绝对路径；CLI 挪窝/重装后重跑 `beaver autostart on` 即可。
// 对应 TS packages/cli/src/autostart.ts。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	winRunKey     = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	winValueName  = "Beaver"
	macLabel      = "ai.beaver.daemon"
	linuxUnitName = "beaver.service"
)

// exePath CLI 真实入口（符号链接解析到实际文件）。
func exePath() string {
	raw, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(raw); err == nil {
		return resolved
	}
	return raw
}

func startArgs(homeOverride string) []string {
	args := []string{"start"}
	if homeOverride != "" {
		args = append(args, "--home", homeOverride)
	}
	return args
}

func vbsPath() string {
	return filepath.Join(filepath.Dir(exePath()), "beaver-autostart.vbs")
}

func macPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", macLabel+".plist")
}

func linuxUnitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", linuxUnitName)
}

func run(cmd string, args []string, quiet bool) (bool, string) {
	c := exec.Command(cmd, args...)
	c.Env = append(os.Environ(), "WINDIR="+os.Getenv("WINDIR"))
	hideWindow(c)
	out, err := c.CombinedOutput()
	if err != nil && !quiet {
		return false, string(out)
	}
	return err == nil, strings.TrimSpace(string(out))
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func installWindows(homeOverride string) (string, error) {
	exe := exePath()
	if exe == "" {
		return "", fmt.Errorf("解析自身可执行文件失败")
	}
	// VBS 以隐藏窗口（0）启动 start 命令；On Error 抑制 wscript 弹窗（如 CLI 被移动的瞬间）
	cmdLine := fmt.Sprintf(`"%s" start`, exe)
	if homeOverride != "" {
		cmdLine += fmt.Sprintf(` --home "%s"`, homeOverride)
	}
	vbs := "On Error Resume Next\r\n" +
		fmt.Sprintf("CreateObject(\"WScript.Shell\").Run \"%s\", 0, False\r\n", strings.ReplaceAll(cmdLine, `"`, `""`))
	if err := os.WriteFile(vbsPath(), []byte(vbs), 0o644); err != nil {
		return "", err
	}
	ok, _ := run("reg", []string{"add", winRunKey, "/v", winValueName, "/t", "REG_SZ", "/d", `wscript.exe "` + vbsPath() + `"`, "/f"}, false)
	if !ok {
		return "", fmt.Errorf("写入注册表 Run 键失败")
	}
	return cmdLine, nil
}

func uninstallWindows() error {
	_, _ = run("reg", []string{"delete", winRunKey, "/v", winValueName, "/f"}, true)
	_ = os.Remove(vbsPath())
	return nil
}

func isInstalledWindows() bool {
	ok, _ := run("reg", []string{"query", winRunKey, "/v", winValueName}, true)
	return ok
}

func installMacos(homeOverride string) (string, error) {
	exe := exePath()
	plistPath := macPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return "", err
	}
	args := append([]string{exe}, startArgs(homeOverride)...)
	entries := ""
	for _, a := range args {
		entries += "\n      <string>" + escapeXML(a) + "</string>"
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key><string>` + macLabel + `</string>
    <key>ProgramArguments</key>
    <array>` + entries + `
    </array>
    <key>RunAtLoad</key><true/>
  </dict>
</plist>
`
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return "", err
	}
	run("launchctl", []string{"unload", plistPath}, true) // 幂等重装
	ok, _ := run("launchctl", []string{"load", plistPath}, false)
	if !ok {
		return "", fmt.Errorf("launchctl load 失败")
	}
	return strings.Join(args, " "), nil
}

func uninstallMacos() error {
	plistPath := macPlistPath()
	if _, err := os.Stat(plistPath); err == nil {
		run("launchctl", []string{"unload", plistPath}, true)
	}
	_ = os.Remove(plistPath)
	return nil
}

func installLinux(homeOverride string) (string, error) {
	exe := exePath()
	unitPath := linuxUnitPath()
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return "", err
	}
	all := append([]string{exe}, startArgs(homeOverride)...)
	quoted := make([]string, len(all))
	for i, a := range all {
		quoted[i] = `"` + a + `"`
	}
	execStr := strings.Join(quoted, " ")
	unit := `[Unit]
Description=Beaver daemon (dam start)

[Service]
Type=oneshot
ExecStart=` + execStr + `

[Install]
WantedBy=default.target
`
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return "", err
	}
	run("systemctl", []string{"--user", "daemon-reload"}, true)
	ok, _ := run("systemctl", []string{"--user", "enable", linuxUnitName}, false)
	if !ok {
		return "", fmt.Errorf("systemctl enable 失败")
	}
	return execStr, nil
}

func uninstallLinux() error {
	run("systemctl", []string{"--user", "disable", linuxUnitName}, true)
	_ = os.Remove(linuxUnitPath())
	run("systemctl", []string{"--user", "daemon-reload"}, true)
	return nil
}

func cmdAutostart(args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "on":
		var cmdline string
		var err error
		switch runtime.GOOS {
		case "windows":
			cmdline, err = installWindows(homeOverride)
		case "darwin":
			cmdline, err = installMacos(homeOverride)
		case "linux":
			cmdline, err = installLinux(homeOverride)
		default:
			return fmt.Errorf("暂不支持的平台: %s（自启支持 Windows / macOS / Linux）", runtime.GOOS)
		}
		if err != nil {
			return err
		}
		fmt.Println("已安装开机自启，下次登录自动拉起 daemon")
		fmt.Printf("  %s\n", cmdline)
		fmt.Println("  撤销: beaver autostart off")
		return nil
	case "off":
		switch runtime.GOOS {
		case "windows":
			err := uninstallWindows()
			fmt.Println("已移除开机自启")
			return err
		case "darwin":
			err := uninstallMacos()
			fmt.Println("已移除开机自启")
			return err
		case "linux":
			err := uninstallLinux()
			fmt.Println("已移除开机自启")
			return err
		}
		return nil
	case "status":
		installed := false
		switch runtime.GOOS {
		case "windows":
			installed = isInstalledWindows()
		case "darwin":
			_, err := os.Stat(macPlistPath())
			installed = err == nil
		case "linux":
			_, err := os.Stat(linuxUnitPath())
			installed = err == nil
		}
		if installed {
			fmt.Println("已安装")
		} else {
			fmt.Println("未安装（beaver autostart on 安装）")
		}
		return nil
	default:
		return fmt.Errorf("未知子命令: autostart %s", sub)
	}
}
