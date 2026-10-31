package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hxgh776/beaver/internal/adapters"
	"github.com/hxgh776/beaver/internal/environment"
	"github.com/hxgh776/beaver/internal/registry"
	"github.com/hxgh776/beaver/internal/skills"
	"github.com/hxgh776/beaver/internal/store"
	"github.com/hxgh776/beaver/internal/syncengine"
	"github.com/hxgh776/beaver/internal/webui"
	"github.com/hxgh776/beaver/internal/writeengine"
)

// Version daemon 版本（与 CLI/package.json 统一）。
const Version = "0.1.0"

// GatewayStatus 网关运行态（Port nil = 未运行）。
type GatewayStatus struct {
	Running bool `json:"running"`
	Port    *int `json:"port,omitempty"`
}

// UpstreamControlResult 上游进程控制返回。
type UpstreamControlResult struct {
	Ok    bool   `json:"ok"`
	State string `json:"state,omitempty"`
}

// ToolInfo 上游工具（含启用态）。
type ToolInfo struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Enabled     bool    `json:"enabled"`
}

// —— 网关钩子（由 gateway 模块注入；缺省时相关 API 返回 503，核心保持协议无关） —— //

type (
	// GatewayControlHook start/stop 网关。
	GatewayControlHook func(action string) (GatewayStatus, error)
	// GatewayHTTPHook 网关 HTTP 处理；返回 true 表示已处理。
	GatewayHTTPHook func(w http.ResponseWriter, r *http.Request, pathname string, query url.Values) bool
	// GatewayStatusHook 查询网关状态。
	GatewayStatusHook func() GatewayStatus
	// UpstreamControlHook 上游进程 start/stop/restart。
	UpstreamControlHook func(id string, action string) (UpstreamControlResult, error)
	// ProbeToolsHook 抓取上游工具列表；refresh=false 优先返回缓存；nil = 失败。
	ProbeToolsHook func(id string, refresh bool) []ToolInfo
	// UpstreamLogsHook 上游 stderr 环形缓冲。
	UpstreamLogsHook func(id string) []string
	// RecentCallsHook 最近调用记录（任意可 JSON 序列化结构）。
	RecentCallsHook func() any
	// InvokeToolHook M3.1 调试台：直接调用工具。
	InvokeToolHook func(id string, tool string, args map[string]any) (any, error)
)

// Options daemon 构造选项。
type Options struct {
	// HomeDir 数据目录（缺省 ~/.beaver）。
	HomeDir string
	// AgentConfigRoot agent 配置文件根目录（缺省为真实用户主目录）。
	// 测试/开发必须传沙箱目录，否则分发会写入真实 agent 配置！
	// 也可用环境变量 AGENT_CONTROL_AGENT_HOME（兼容旧名 MCP_MANAGER_AGENT_HOME）指定。
	AgentConfigRoot string
	// Port nil = 用 config.settings.port（端口漂移会持久化）；0 = 临时端口（不持久化）；N = 固定端口。
	Port *int
	// WebDistDir nil = 内嵌资源；非 nil = 从该目录读（开发调试用）。
	WebDistDir *string
}

// Daemon 单进程承载 HTTP 服务与配置内存态。
type Daemon struct {
	Store  *store.Store
	Logger *Logger
	Router *Router
	Config *store.Config

	// 网关钩子：Start 后由 gateway.Attach 注入
	GatewayControl  GatewayControlHook
	GatewayHTTP     GatewayHTTPHook
	GatewayStatusFn GatewayStatusHook
	// Skills 技能管理服务（M2）
	Skills *skills.Service
	// GatewaySnapshots 上游运行态快照（网关注入）
	GatewaySnapshots func() any
	UpstreamControl  UpstreamControlHook
	ProbeTools       ProbeToolsHook
	UpstreamLogs     UpstreamLogsHook
	RecentCalls      RecentCallsHook
	InvokeTool       InvokeToolHook

	opts       Options
	syncOpts   syncengine.Options
	agentRoot  string
	listener   net.Listener
	httpServer *http.Server
	startedAt  time.Time
	actualPort int
	agentMu    sync.Mutex
	agentCache []registry.AgentDetection
	stopOnce   sync.Once
}

// New 创建 daemon（不绑定端口）；bridgeCommand/baseArgs 为 stdio 反向桥调用方式，
// 缺省用自身可执行文件 + ["bridge"]。
func New(opts Options) *Daemon {
	st := store.NewStore(opts.HomeDir)
	logger := NewLogger(filepath.Join(st.LogsDir(), "daemon.log"), "")
	agentRoot := opts.AgentConfigRoot
	if agentRoot == "" {
		agentRoot = os.Getenv("AGENT_CONTROL_AGENT_HOME")
	}
	if agentRoot == "" {
		agentRoot = os.Getenv("MCP_MANAGER_AGENT_HOME")
	}
	d := &Daemon{
		Store:     st,
		Logger:    logger,
		Router:    &Router{},
		opts:      opts,
		startedAt: time.Now(),
	}
	if agentRoot != "" {
		d.agentRoot = agentRoot
	}
	// G7：stdio 反向桥与 daemon 同可执行文件（beaver bridge 子命令）
	exe, err := os.Executable()
	if err == nil {
		d.syncOpts.StdioBridge = &syncengine.StdioBridge{Command: exe, BaseArgs: []string{"bridge"}}
	}
	d.syncOpts.BackupsDir = st.BackupsDir()
	d.syncOpts.HomeDir = st.HomeDir
	if agentRoot != "" {
		d.syncOpts.Agents = syncengine.DefaultAgentRefs(agentRoot)
	} else if opts.HomeDir != "" || os.Getenv("AGENT_CONTROL_HOME") != "" || os.Getenv("MCP_MANAGER_HOME") != "" {
		logger.Warn("数据目录已重定向但 agent 配置仍指向真实用户主目录——测试场景请传 agentConfigRoot")
	}
	d.Skills = skills.NewService(skills.ServiceOptions{
		HomeDir:    st.HomeDir,
		GetConfig:  func() *store.Config { return d.Config },
		SaveConfig: func() error { return d.SaveConfig() },
		AgentRoot:  agentRoot,
	})
	return d
}

// ActualPort 实际绑定端口（start 后有效）。
func (d *Daemon) ActualPort() int { return d.actualPort }

// BaseURL 环回基地址。
func (d *Daemon) BaseURL() string { return fmt.Sprintf("http://127.0.0.1:%d", d.actualPort) }

// Start 加载配置、注册路由、绑定端口并开始服务（后台 goroutine 服务请求）。
func (d *Daemon) Start() error {
	config, err := d.Store.Load()
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	d.Config = config
	d.Logger.SetLevel(config.Settings.LogLevel)
	registerAPIRoutes(d.Router, d)

	desired := config.Settings.Port
	if d.opts.Port != nil {
		desired = *d.opts.Port
	}
	var lastErr error
	bound := false
	for i := 0; i <= 10; i++ {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", desired+i))
		if err == nil {
			d.listener = l
			d.actualPort = l.Addr().(*net.TCPAddr).Port
			bound = true
			break
		}
		lastErr = err
		if !isAddrInUse(err) {
			break
		}
	}
	if !bound {
		if lastErr == nil {
			lastErr = errors.New("无法绑定端口")
		}
		return lastErr
	}

	// 端口漂移时持久化（显式/临时端口除外）
	if d.opts.Port == nil && d.actualPort != config.Settings.Port {
		config.Settings.Port = d.actualPort
		if err := d.SaveConfig(); err != nil {
			d.Logger.Warn("端口漂移持久化失败", map[string]any{"err": err.Error()})
		}
	}

	// S0：崩溃前滚——判定上次未清账的发布意图（只上报，不改文件内容）
	recovered, err := writeengine.RecoverPendingPublications(d.Store.HomeDir)
	if err != nil {
		d.Logger.Warn("发布日志前滚判定失败", map[string]any{"err": err.Error()})
	}
	for _, r := range recovered {
		d.Logger.Warn(fmt.Sprintf("上次发布未正常收账: %s → %s", r.Path, r.State))
	}

	d.httpServer = &http.Server{Handler: http.HandlerFunc(d.handle)}
	// 网关钩子常驻挂载；运行时开关见 /api/gateway/start|stop（由 gateway.Attach 注入）
	if d.GatewayStatusFn == nil {
		d.GatewayStatusFn = func() GatewayStatus { return GatewayStatus{} }
	}
	d.Logger.Info(fmt.Sprintf("daemon started at %s (home: %s)", d.BaseURL(), d.Store.HomeDir))
	go func() {
		if err := d.httpServer.Serve(d.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.Logger.Error("HTTP 服务异常退出", map[string]any{"err": err.Error()})
		}
	}()
	return nil
}

// Stop 停止 HTTP 服务（网关 detach 由调用方/CLI 层先做）。
func (d *Daemon) Stop() error {
	var err error
	d.stopOnce.Do(func() {
		if d.httpServer == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = d.httpServer.Shutdown(ctx)
		d.httpServer = nil
		d.Logger.Info("daemon stopped")
	})
	return err
}

// SaveConfig 持久化配置。
func (d *Daemon) SaveConfig() error { return d.Store.Save(d.Config) }

// Sync 执行分发（调用方决定是否落盘 config）。
func (d *Daemon) Sync(dryRun bool, resolutions map[string]adapters.ConflictResolution) (syncengine.Report, error) {
	return syncengine.Sync(d.Config, d.syncOpts, syncengine.Input{DryRun: dryRun, Resolutions: resolutions})
}

// AgentDetections agent 探测（带缓存；force 强制重探）。
func (d *Daemon) AgentDetections(force bool) ([]registry.AgentDetection, error) {
	d.agentMu.Lock()
	defer d.agentMu.Unlock()
	if d.agentCache == nil || force {
		d.agentCache = registry.DetectAgents(syncengine.DefaultAgentRefs(d.agentRoot))
	}
	return d.agentCache, nil
}

// UptimeSec 运行秒数。
func (d *Daemon) UptimeSec() int { return int(time.Since(d.startedAt).Seconds()) }

// DetectEnvironment S5 环境检测。
func (d *Daemon) DetectEnvironment() []environment.RuntimeTool { return environment.Detect() }

func (d *Daemon) handle(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.RequestURI()
	u, err := url.Parse(raw)
	if err != nil {
		SendError(w, 400, "非法 URL")
		return
	}
	pathname := u.EscapedPath()
	query := u.Query()
	defer func() {
		if rec := recover(); rec != nil {
			d.Logger.Error("request panic", map[string]any{"pathname": pathname, "err": fmt.Sprint(rec)})
			if !isHeaderWritten(w) {
				SendError(w, 500, fmt.Sprint(rec))
			}
		}
	}()
	// 1) MCP 网关端点（含 per-server 与 per-agent 分组端点）
	if pathname == "/mcp" || strings.HasPrefix(pathname, "/servers/") || strings.HasPrefix(pathname, "/agents/") {
		if d.GatewayHTTP != nil && d.GatewayHTTP(w, r, pathname, query) {
			return
		}
		SendError(w, 503, "gateway 未启动", "GATEWAY_DOWN")
		return
	}

	// 2) 管理 API（D7：authRequired 开启时校验 Bearer token；/api/auth/info 永远公开）
	if pathname == "/api" || strings.HasPrefix(pathname, "/api/") {
		isPublic := pathname == "/api/auth/info"
		if !isPublic && d.Config.Settings.AuthRequired != nil && *d.Config.Settings.AuthRequired &&
			!CheckToken(r, d.Config.Settings.Token, query) {
			SendError(w, 401, "未授权", "UNAUTHORIZED")
			return
		}
		if !d.Router.Dispatch(r, w, pathname, query) {
			SendError(w, 404, "not found")
		}
		return
	}

	// 3) Web UI 静态资源
	d.serveStatic(pathname, w)
}

func (d *Daemon) serveStatic(pathname string, w http.ResponseWriter) {
	var content fs.FS
	if d.opts.WebDistDir != nil {
		content = os.DirFS(*d.opts.WebDistDir)
	} else {
		sub, err := fs.Sub(webui.Dist, "dist")
		if err != nil {
			SendError(w, 404, "Web UI 未安装（web/dist 缺失），可用 CLI 或 /api 操作")
			return
		}
		content = sub
	}
	name := pathname
	if name == "/" || name == "" {
		name = "index.html"
	}
	name = path.Clean(strings.TrimPrefix(name, "/"))
	data, err := fs.ReadFile(content, name)
	if err != nil {
		// SPA 路由回退：无扩展名的路径回退到 index.html
		if path.Ext(name) == "" {
			data, err = fs.ReadFile(content, "index.html")
			name = "index.html"
		}
		if err != nil {
			SendError(w, 404, "not found")
			return
		}
	}
	w.Header().Set("Content-Type", contentTypeOf(name))
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	_, _ = w.Write(data)
}

func contentTypeOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}

// isHeaderWritten 用一个空写探测响应头是否已发（net/http 无公开查询接口）。
func isHeaderWritten(w http.ResponseWriter) bool {
	type writer interface{ Written() bool }
	if hw, ok := w.(writer); ok {
		return hw.Written()
	}
	return false
}
