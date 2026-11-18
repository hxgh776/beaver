// Beaver（河狸）——本地 Agent 装备总控台。
// 单文件 CLI + daemon：子命令与 TS 版 dam 一一对应（M1.4/M1.5 移植）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hxgh776/beaver/internal/daemon"
	"github.com/hxgh776/beaver/internal/desktop"
	"github.com/hxgh776/beaver/internal/gateway"
	"github.com/hxgh776/beaver/internal/store"
	"github.com/hxgh776/beaver/internal/version"
)

// 全局 flag：入口层统一摘除（extractGlobalFlags），daemon / TUI 等入口共用。
var (
	homeOverride      string
	agentHomeOverride string
	portOverride      *int
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// desktopEntry 无参数入口的桌面分派（变量形式便于参数层测试替换，避免测试真起托盘/窗口）。
var desktopEntry = cmdDesktop

// run 入口分派；返回进程退出码（os.Exit 收敛在 main，便于参数层回归测试）。
func run(argv []string) int {
	args := extractGlobalFlags(argv)
	if len(args) == 0 {
		// 无参数（或仅全局 flag）= 桌面面板（双击 exe 即见界面）：
		// 托盘常驻 + 独立应用窗口；终端界面改由 `beaver tui` 显式进入。
		if err := desktopEntry(); err != nil {
			desktopFailure(err)
			return 1
		}
		return 0
	}
	var err error
	holdOnFailure := false
	switch args[0] {
	case "tui":
		err = cmdTui()
		holdOnFailure = true
	case "start":
		err = cmdStart()
	case "stop":
		err = cmdStop()
	case "status":
		err = cmdStatus()
	case "daemon":
		err = runDaemon() // 内部子命令：分离式后台进程入口
	case "__panel":
		err = cmdPanel() // 内部子命令：分离式原生面板窗口进程
	case "servers":
		err = cmdServers(args[1:])
	case "agents":
		err = cmdAgents()
	case "import":
		err = cmdImport(args[1:])
	case "sync":
		err = cmdSync(args[1:])
	case "bridge":
		err = cmdBridge(args[1:])
	case "gateway":
		err = cmdGateway(args[1:])
	case "autostart":
		err = cmdAutostart(args[1:])
	case "tray":
		err = cmdTray()
	case "open":
		err = cmdOpen()
	case "version":
		fmt.Println("beaver", version.String())
	default:
		usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err.Error())
		if holdOnFailure {
			holdTuiFailureWindow()
		}
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprint(os.Stderr, `beaver（dam）——本地 Agent 能力总控台

用法: beaver [--home <dir>] [--agent-home <dir>] [--port <n>] <command>

  （无参数）         打开桌面面板（托盘常驻 + 独立应用窗口；daemon 未跑则自动拉起）
  tui                进入终端面板 TUI（无 GUI 环境 / 远程终端时使用）
  start              启动 daemon（分离式后台进程）
  stop               停止 daemon
  status             查看 daemon 状态
  servers list|add   MCP server 注册表
  agents             查看探测到的 agent
  import [--all]     从现有 agent 配置导入 server 定义
  sync [--dry-run]   把注册表分发到各 agent
  gateway status|start|stop   MCP 网关
  bridge <url>       stdio 反向桥（把 HTTP 上游暴露为本地 stdio server）
  autostart on|off|status    开机自启 daemon
  tray               托盘常驻（自持有 daemon；已跑则挂远端）
  open               打开桌面面板窗口（原生 WebView2 壳；不可用时回退浏览器）
  version            版本信息
`)
}

// extractGlobalFlags 摘除全局 flag --home / --agent-home / --port
// （位置不限、= 形式等价，commander 全局选项语义；VENU-16 复审修复：
// 之前只摘 --home，`beaver --home X --agent-home Y` 会把 --agent-home 当未知子命令）。
// --port 值非法时不摘除，留给 usage 报错路径（exit 2）。
func extractGlobalFlags(args []string) []string {
	out := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--home" && i+1 < len(args):
			homeOverride = args[i+1]
			i++
		case strings.HasPrefix(arg, "--home="):
			homeOverride = strings.TrimPrefix(arg, "--home=")
		case arg == "--agent-home" && i+1 < len(args):
			agentHomeOverride = args[i+1]
			i++
		case strings.HasPrefix(arg, "--agent-home="):
			agentHomeOverride = strings.TrimPrefix(arg, "--agent-home=")
		case arg == "--port" && i+1 < len(args):
			if p, err := strconv.Atoi(args[i+1]); err == nil {
				portOverride = &p
				i++
			} else {
				out = append(out, arg, args[i+1])
				i++
			}
		case strings.HasPrefix(arg, "--port="):
			if p, err := strconv.Atoi(strings.TrimPrefix(arg, "--port=")); err == nil {
				portOverride = &p
			} else {
				out = append(out, arg)
			}
		default:
			out = append(out, arg)
		}
	}
	return out
}

func newStore() *store.Store { return store.NewStore(homeOverride) }

// api 请求本机 daemon REST API；daemon 不在线时报错退出。
func api(method, pathname string, body any) (int, map[string]any, error) {
	s := newStore()
	config, err := s.Load()
	if err != nil {
		return 0, nil, fmt.Errorf("加载配置失败: %w", err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", config.Settings.Port)
	var rdr io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, base+pathname, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("authorization", "Bearer "+config.Settings.Token)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	client := &http.Client{Timeout: 300 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("daemon 未运行（%s），请先执行 beaver start", base)
	}
	defer res.Body.Close()
	var data map[string]any
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		return res.StatusCode, nil, err
	}
	return res.StatusCode, data, nil
}

func cmdStart() error {
	s := newStore()
	if _, err := s.Load(); err != nil { // 确保配置与 token 存在
		return err
	}
	pidPath := s.PidPath()
	if pid, _ := daemon.ReadPid(pidPath); pid > 0 && daemon.IsPidAlive(pid) {
		config, _ := s.Load()
		fmt.Printf("daemon 已在运行 (pid %d)：http://127.0.0.1:%d\n", pid, config.Settings.Port)
		return nil
	}
	childPid, err := daemon.SpawnDetached(homeOverride, portOverride, agentHomeOverride)
	if err != nil {
		return err
	}
	config, _ := s.Load()
	desired := config.Settings.Port
	if portOverride != nil {
		desired = *portOverride
	}
	client := &http.Client{Timeout: 1 * time.Second}
	base := ""
	for i := 0; i < 50 && base == ""; i++ {
		for offset := 0; offset <= 10; offset++ {
			candidate := fmt.Sprintf("http://127.0.0.1:%d", desired+offset)
			req, _ := http.NewRequest("GET", candidate+"/api/status", nil)
			req.Header.Set("authorization", "Bearer "+config.Settings.Token)
			if res, err := client.Do(req); err == nil {
				_ = res.Body.Close()
				if res.StatusCode == http.StatusOK {
					base = candidate
					break
				}
			}
		}
		if base == "" {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if base != "" {
		fmt.Printf("daemon 已启动 (pid %d)\n", childPid)
		fmt.Printf("  API/UI: %s\n", base)
		fmt.Printf("  MCP   : %s/mcp\n", base)
		return nil
	}
	logPath := filepath.Join(s.LogsDir(), "daemon.log")
	return fmt.Errorf("daemon 启动超时，请查看日志: %s", logPath)
}

func cmdStop() error {
	s := newStore()
	pidPath := s.PidPath()
	pid, _ := daemon.ReadPid(pidPath)
	if pid <= 0 || !daemon.IsPidAlive(pid) {
		daemon.RemovePidFile(pidPath)
		fmt.Println("daemon 未在运行")
		return nil
	}
	if err := daemon.KillTree(pid); err != nil {
		return err
	}
	daemon.RemovePidFile(pidPath)
	fmt.Printf("daemon 已停止 (pid %d)\n", pid)
	return nil
}

func cmdStatus() error {
	status, data, err := api("GET", "/api/status", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("status %d", status)
	}
	out, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(out))
	return nil
}

// runDaemon daemon 子命令：常驻进程主体（由 start 分离式拉起）。
// 支持 --home / --agent-home / --port 参数（对齐 TS daemon 入口），
// 全部经入口层 extractGlobalFlags 摘入全局变量，此处不再二次解析。
func runDaemon() error {
	opts := daemon.Options{HomeDir: homeOverride, AgentConfigRoot: agentHomeOverride, Port: portOverride}
	d := daemon.New(opts)
	gw := gateway.New(d)
	gw.Attach()
	if err := d.Start(); err != nil {
		return err
	}
	// PID 文件：start/stop 的进程管理依据
	if err := os.WriteFile(daemon.PidFilePath(d.Store.HomeDir), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return fmt.Errorf("写入 PID 文件失败: %w", err)
	}
	fmt.Printf("beaver daemon v%s\n", version.String())
	fmt.Printf("  API/UI : %s\n", d.BaseURL())
	fmt.Printf("  MCP    : %s/mcp (gateway 启动后可用)\n", d.BaseURL())
	fmt.Printf("  home   : %s\n", d.Store.HomeDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	<-ctx.Done()
	d.Logger.Info("received SIGINT, shutting down")
	_ = gw.Detach()
	_ = d.Stop()
	_ = os.Remove(daemon.PidFilePath(d.Store.HomeDir))
	return nil
}

func cmdServers(args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list":
		_, data, err := api("GET", "/api/servers", nil)
		if err != nil {
			return err
		}
		servers, _ := data["servers"].([]any)
		if len(servers) == 0 {
			fmt.Println("（空）先用 beaver import 或 servers add 添加")
			return nil
		}
		for _, raw := range servers {
			s, _ := raw.(map[string]any)
			fmt.Printf("%-24s %-6s gateway=%v enabled=%v  %v\n",
				s["id"], s["transport"], s["gatewayMode"], s["enabled"], s["name"])
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("servers add", flag.ContinueOnError)
		name := fs.String("name", "", "名称")
		transport := fs.String("transport", "", "stdio | http")
		command := fs.String("command", "", "stdio 命令")
		url := fs.String("url", "", "http 地址")
		gatewayMode := fs.Bool("gateway", false, "使用网关模式分发")
		envList := arrayFlag(fs, "env", "环境变量（可重复 key=value）")
		fs.SetOutput(os.Stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || *transport == "" {
			return fmt.Errorf("--name 与 --transport 必填")
		}
		env := map[string]string{}
		for _, kv := range *envList {
			if idx := strings.Index(kv, "="); idx > 0 {
				env[kv[:idx]] = kv[idx+1:]
			}
		}
		body := map[string]any{
			"name":        *name,
			"transport":   *transport,
			"command":     *command,
			"env":         env,
			"url":         *url,
			"gatewayMode": *gatewayMode,
		}
		if len(env) == 0 {
			delete(body, "env")
		}
		status, data, err := api("POST", "/api/servers", body)
		if err != nil {
			return err
		}
		if status != http.StatusCreated {
			return fmt.Errorf("添加失败: %v", data)
		}
		server, _ := data["server"].(map[string]any)
		fmt.Printf("已添加: %v\n", server["id"])
		return nil
	default:
		return fmt.Errorf("未知子命令: servers %s", sub)
	}
}

func cmdAgents() error {
	_, data, err := api("GET", "/api/agents?refresh=1", nil)
	if err != nil {
		return err
	}
	agents, _ := data["agents"].([]any)
	for _, raw := range agents {
		a, _ := raw.(map[string]any)
		mark := "○"
		if b, _ := a["detected"].(bool); b {
			mark = "●"
		}
		bound, _ := a["boundServerIds"].([]any)
		boundStrs := make([]string, 0, len(bound))
		for _, v := range bound {
			boundStrs = append(boundStrs, fmt.Sprint(v))
		}
		display := padRight(fmt.Sprint(a["displayName"]), 14)
		atype := padRight(fmt.Sprint(a["agentType"]), 14)
		fmt.Printf("%s %s %s 绑定: %s\n", mark, display, atype, strings.Join(boundStrs, ", "))
	}
	return nil
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// arrayFlag 可重复的字符串 flag（commander 的可重复选项等价）。
func arrayFlag(fs *flag.FlagSet, name, usage string) *[]string {
	out := []string{}
	fs.Func(name, usage, func(v string) error {
		out = append(out, v)
		return nil
	})
	return &out
}

func cmdImport(args []string) error {
	all := false
	for _, a := range args {
		if a == "--all" {
			all = true
		}
	}
	_, data, err := api("GET", "/api/import/preview", nil)
	if err != nil {
		return err
	}
	groups, _ := data["groups"].([]any)
	pending := []map[string]any{}
	for _, raw := range groups {
		g, _ := raw.(map[string]any)
		if imported, _ := g["alreadyImported"].(bool); !imported {
			pending = append(pending, g)
		}
	}
	if len(pending) == 0 {
		fmt.Println("没有可导入的新 server")
		return nil
	}
	fingerprints := []string{}
	for _, g := range pending {
		suggested, _ := g["suggested"].(map[string]any)
		candidates, _ := g["candidates"].([]any)
		fmt.Printf("%-24v ← %d 处  %v\n", suggested["name"], len(candidates), g["fingerprint"])
		fingerprints = append(fingerprints, fmt.Sprint(g["fingerprint"]))
	}
	if !all {
		fmt.Println("\n（预览模式，执行 beaver import --all 以导入）")
		return nil
	}
	status, data, err := api("POST", "/api/import", map[string]any{"fingerprints": fingerprints})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("导入失败: %v", data)
	}
	added, _ := data["added"].([]any)
	fmt.Printf("已导入 %d 个 server\n", len(added))
	return nil
}

func cmdSync(args []string) error {
	dryRun := false
	for _, a := range args {
		if a == "--dry-run" {
			dryRun = true
		}
	}
	_, data, err := api("POST", "/api/sync", map[string]any{"dryRun": dryRun})
	if err != nil {
		return err
	}
	report, _ := data["report"].(map[string]any)
	perAgent, _ := report["perAgent"].([]any)
	for _, raw := range perAgent {
		agent, _ := raw.(map[string]any)
		changes, _ := agent["changes"].([]any)
		conflicts, _ := agent["conflicts"].([]any)
		unsupported, _ := agent["unsupported"].([]any)
		active := 0
		lines := []string{}
		for _, c := range changes {
			cm, _ := c.(map[string]any)
			action, _ := cm["action"].(string)
			if action == "none" {
				continue
			}
			active++
			lines = append(lines, fmt.Sprintf("  %-7s %v", action, cm["key"]))
		}
		if active == 0 && len(conflicts) == 0 && len(unsupported) == 0 {
			continue
		}
		fmt.Printf("[%v]\n", agent["agentType"])
		for _, l := range lines {
			fmt.Println(l)
		}
		for _, c := range conflicts {
			cm, _ := c.(map[string]any)
			fmt.Printf("  ⚠ 冲突 %v（在 TUI 同步页处置或覆盖）\n", cm["key"])
		}
		for _, u := range unsupported {
			um, _ := u.(map[string]any)
			fmt.Printf("  ! 跳过 %v: %v\n", um["serverId"], um["reason"])
		}
	}
	if dryRun {
		fmt.Println("\n（dry-run，未落盘）")
	} else {
		fmt.Println("\n同步完成")
	}
	return nil
}

func cmdBridge(args []string) error {
	opts, err := gateway.ParseBridgeArgs(args)
	if err != nil {
		return err
	}
	return gateway.RunBridgeStdio(opts)
}

func cmdGateway(args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "status":
		_, data, err := api("GET", "/api/gateway", nil)
		if err != nil {
			return err
		}
		if running, _ := data["running"].(bool); running {
			fmt.Printf("网关运行中: http://127.0.0.1:%v/mcp\n", data["port"])
		} else {
			fmt.Println("网关未运行")
		}
		return nil
	case "start", "stop":
		status, data, err := api("POST", "/api/gateway/"+sub, nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			msg, _ := data["error"].(string)
			return fmt.Errorf("%s", msg)
		}
		if running, _ := data["running"].(bool); running {
			fmt.Printf("网关已启动: http://127.0.0.1:%v/mcp\n", data["port"])
		} else {
			fmt.Println("网关已停止")
		}
		return nil
	default:
		return fmt.Errorf("未知子命令: gateway %s", sub)
	}
}

// cmdOpen 打开面板窗口（open 语义 = 确保 daemon 在跑再开窗，docs/desktop-plan.md §4）。
// Windows 由分离式 `__panel` 子进程承载原生 WebView2 窗口，无浏览器进程参与。
func cmdOpen() error {
	base, err := desktop.EnsureRunning(homeOverride, portOverride, agentHomeOverride)
	if err != nil {
		return err
	}
	if err := desktop.LaunchPanel(homeOverride, portOverride, agentHomeOverride, base); err != nil {
		return err
	}
	fmt.Printf("已打开面板 %s\n", base)
	return nil
}
