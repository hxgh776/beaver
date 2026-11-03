// Package gateway 实现 MCP 网关：上游连接管理、聚合/分组/per-server 端点、stdio 反向桥。
// 对应 TS packages/server/src/gateway/*，借宿 daemon 的 HTTP 端口（无独立端口）。
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hxgh776/beaver/internal/autostart"
	"github.com/hxgh776/beaver/internal/secretbox"
	"github.com/hxgh776/beaver/internal/store"
)

const (
	connectTimeout = 15 * time.Second
	callTimeout    = 120 * time.Second
	pingTimeout    = 5 * time.Second
	maxBackoff     = 30 * time.Second
	stderrRingSize = 200
)

type upstreamStatus string

const (
	statusIdle     upstreamStatus = "idle"
	statusStarting upstreamStatus = "starting"
	statusReady    upstreamStatus = "ready"
	statusError    upstreamStatus = "error"
	statusBackoff  upstreamStatus = "backoff"
)

// UpstreamSnapshot 管理页上游运行态快照。
type UpstreamSnapshot struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	PID       *int    `json:"pid,omitempty"`
	ToolCount int     `json:"toolCount"`
	LastError *string `json:"lastError"`
}

// Upstream 单个上游 MCP server 的连接封装。
// - 懒启动：首次使用才拉起进程/建连（G2）
// - 崩溃自愈：指数退避重启（R6）
// - stdio 串行化：上游多为单会话设计，请求排队执行（R3/G12 默认策略）
type Upstream struct {
	Def *store.ServerDef

	mu        sync.Mutex
	status    upstreamStatus
	lastError *string
	pid       *int
	tools     []*mcp.Tool // 客户端可见的工具原始列表（未经聚合过滤）
	session   *mcp.ClientSession
	used      bool

	connectMu       sync.Mutex
	connecting      bool
	restartAttempts int
	restartTimer    *time.Timer

	sem chan struct{} // M3.2 G12：并发信号量（默认 1 = 串行）

	stderrMu   sync.Mutex
	stderrRing []string
}

func newUpstream(def *store.ServerDef) *Upstream {
	limit := 1
	if def.Concurrency != nil && *def.Concurrency > 1 {
		limit = *def.Concurrency
	}
	return &Upstream{Def: def, status: statusIdle, sem: make(chan struct{}, limit)}
}

func (u *Upstream) snapshotStatus() (upstreamStatus, *string, *int, int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status, u.lastError, u.pid, len(u.tools)
}

func (u *Upstream) setStatus(s upstreamStatus) {
	u.mu.Lock()
	u.status = s
	u.mu.Unlock()
}

func (u *Upstream) StderrLines() []string {
	u.stderrMu.Lock()
	defer u.stderrMu.Unlock()
	out := make([]string, len(u.stderrRing))
	copy(out, u.stderrRing)
	return out
}

// EnsureClient 懒建连（并发调用合并为一次）。
func (u *Upstream) EnsureClient(ctx context.Context) (*mcp.ClientSession, error) {
	u.mu.Lock()
	if u.session != nil {
		s := u.session
		u.mu.Unlock()
		return s, nil
	}
	u.mu.Unlock()

	u.connectMu.Lock()
	defer u.connectMu.Unlock()
	u.mu.Lock()
	if u.session != nil {
		s := u.session
		u.mu.Unlock()
		return s, nil
	}
	u.used = true
	u.status = statusStarting // 持锁直写，禁止调用 setStatus（不可重入）
	u.mu.Unlock()

	s, err := u.connect(ctx)
	if err != nil {
		u.mu.Lock()
		u.status = statusError
		msg := err.Error()
		u.lastError = &msg
		u.mu.Unlock()
		return nil, err
	}
	u.mu.Lock()
	u.session = s
	u.status = statusReady
	u.lastError = nil
	u.restartAttempts = 0
	u.mu.Unlock()

	// 连接关闭 → 崩溃自愈调度（仅当未被主动 stop）
	go u.watchClose(s)
	return s, nil
}

// watchClose 会话关闭后按 used 状态调度指数退避重连（对应 TS client.onclose）。
func (u *Upstream) watchClose(s *mcp.ClientSession) {
	s.Wait()
	u.connectMu.Lock()
	defer u.connectMu.Unlock()
	u.mu.Lock()
	if u.session != s || !u.used {
		u.mu.Unlock()
		return // 已被 stop() 替换/主动关闭
	}
	u.session = nil
	u.mu.Unlock()
	u.scheduleRestartLocked("连接关闭")
}

func (u *Upstream) connect(ctx context.Context) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "beaver-gateway", Version: "0.1.0"}, nil)
	// S4：静态加密凭证在使用点解密（内存明文，不落盘）
	env := map[string]string{}
	if u.Def.Env != nil {
		dec, err := secretbox.DecryptRecord(*u.Def.Env)
		if err == nil {
			env = dec
		} else {
			env = *u.Def.Env
		}
	}
	headers := map[string]string{}
	if u.Def.Headers != nil {
		dec, err := secretbox.DecryptRecord(*u.Def.Headers)
		if err == nil {
			headers = dec
		} else {
			headers = *u.Def.Headers
		}
	}
	var transport mcp.Transport
	switch u.Def.Transport {
	case store.TransportStdio:
		if u.Def.Command == nil || *u.Def.Command == "" {
			return nil, errors.New("stdio 上游缺少 command")
		}
		cmd := exec.Command(*u.Def.Command, derefSlice(u.Def.Args)...)
		autostart.HideWindow(cmd) // stdio 上游（npx 等）不弹控制台窗
		cmd.Dir = deref(u.Def.Cwd)
		cmd.Env = stdioEnv(env)
		cmd.Stderr = &stderrRingWriter{u: u}
		transport = &mcp.CommandTransport{Command: cmd}
	case store.TransportSSE:
		// M2.3：legacy SSE 上游（D4 兼容）
		if u.Def.URL == nil || *u.Def.URL == "" {
			return nil, errors.New("sse 上游缺少 url")
		}
		transport = &mcp.SSEClientTransport{Endpoint: *u.Def.URL, HTTPClient: httpClientWithHeaders(headers)}
	default:
		if u.Def.URL == nil || *u.Def.URL == "" {
			return nil, errors.New("http 上游缺少 url")
		}
		transport = &mcp.StreamableClientTransport{
			Endpoint:             *u.Def.URL,
			HTTPClient:           httpClientWithHeaders(headers),
			DisableStandaloneSSE: true, // stateless 网关型上游不建独立 SSE 流（与 TS 客户端行为一致）
		}
	}

	cctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	s, err := client.Connect(cctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("连接超时或失败: %w", err)
	}
	if cmd, ok := transport.(*mcp.CommandTransport); ok && cmd.Command.Process != nil {
		pid := cmd.Command.Process.Pid
		u.mu.Lock()
		u.pid = &pid
		u.mu.Unlock()
	}
	return s, nil
}

// ListTools 工具列表（带缓存）；force 强制重抓。
func (u *Upstream) ListTools(ctx context.Context, force bool) ([]*mcp.Tool, error) {
	u.mu.Lock()
	if !force && len(u.tools) > 0 {
		out := u.tools
		u.mu.Unlock()
		return out, nil
	}
	u.mu.Unlock()
	s, err := u.EnsureClient(ctx)
	if err != nil {
		return nil, err
	}
	var tools []*mcp.Tool
	cursor := ""
	for {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		params := &mcp.ListToolsParams{}
		if cursor != "" {
			params.Cursor = cursor
		}
		res, err := s.ListTools(cctx, params)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("listTools 超时: %w", err)
		}
		tools = append(tools, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	u.mu.Lock()
	u.tools = tools
	u.mu.Unlock()
	return tools, nil
}

// CallTool 工具调用；stdio 上游按 concurrency 信号量限流（默认串行）。
func (u *Upstream) CallTool(ctx context.Context, toolName string, args json.RawMessage) (*mcp.CallToolResult, error) {
	if u.Def.Transport != store.TransportStdio {
		s, err := u.EnsureClient(ctx)
		if err != nil {
			return nil, err
		}
		return u.invoke(ctx, s, toolName, args)
	}
	u.sem <- struct{}{}
	defer func() { <-u.sem }()
	s, err := u.EnsureClient(ctx)
	if err != nil {
		return nil, err
	}
	return u.invoke(ctx, s, toolName, args)
}

func (u *Upstream) invoke(ctx context.Context, s *mcp.ClientSession, toolName string, args json.RawMessage) (*mcp.CallToolResult, error) {
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := s.CallTool(cctx, &mcp.CallToolParams{Name: toolName, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("工具 %s 调用超时: %w", toolName, err)
	}
	return res, nil
}

// Health ping 探活。
func (u *Upstream) Health(ctx context.Context) bool {
	s, err := u.EnsureClient(ctx)
	if err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	return s.Ping(cctx, nil) == nil
}

// Stop 主动断开（stop/restart）；不触发自动重启。
func (u *Upstream) Stop() error {
	u.connectMu.Lock()
	if u.restartTimer != nil {
		u.restartTimer.Stop()
		u.restartTimer = nil
	}
	u.connectMu.Unlock()
	u.mu.Lock()
	s := u.session
	u.session = nil
	u.tools = nil
	u.pid = nil
	u.used = false
	u.status = statusIdle
	u.mu.Unlock()
	if s != nil {
		_ = s.Close() // 子进程可能已死，忽略关闭错误
	}
	return nil
}

// scheduleRestartLocked 需持有 connectMu。
func (u *Upstream) scheduleRestartLocked(reason string) {
	u.mu.Lock()
	if !u.used || u.restartTimer != nil {
		u.mu.Unlock()
		return
	}
	delay := 500 * time.Millisecond << uint(u.restartAttempts)
	if delay > maxBackoff {
		delay = maxBackoff
	}
	u.restartAttempts++
	u.status = statusBackoff
	u.mu.Unlock()
	_ = reason // 日志由 gateway 层输出（避免循环依赖 logger 参数）
	u.restartTimer = time.AfterFunc(delay, func() {
		u.connectMu.Lock()
		u.restartTimer = nil
		u.connectMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		_, _ = u.EnsureClient(ctx)
	})
}

// stderrRingWriter 收集上游 stderr 到环形缓冲。
type stderrRingWriter struct {
	u *Upstream
}

func (w *stderrRingWriter) Write(p []byte) (int, error) {
	text := string(p)
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		w.u.stderrMu.Lock()
		w.u.stderrRing = append(w.u.stderrRing, line)
		if len(w.u.stderrRing) > stderrRingSize {
			w.u.stderrRing = w.u.stderrRing[len(w.u.stderrRing)-stderrRingSize:]
		}
		w.u.stderrMu.Unlock()
	}
	return len(p), nil
}

// stdioEnv 复刻 TS SDK getDefaultEnvironment：仅继承安全白名单 + 注入 def.env。
func stdioEnv(inject map[string]string) []string {
	var whitelist []string
	if runtime.GOOS == "windows" {
		whitelist = []string{"APPDATA", "HOMEDRIVE", "HOMEPATH", "LOCALAPPDATA", "PATH", "PROCESSOR_ARCHITECTURE", "SYSTEMDRIVE", "SYSTEMROOT", "TEMP", "USERNAME", "USERPROFILE", "PROGRAMFILES"}
	} else {
		whitelist = []string{"HOME", "LOGNAME", "PATH", "SHELL", "TERM", "USER"}
	}
	env := map[string]string{}
	for _, k := range whitelist {
		if v := os.Getenv(k); v != "" && !strings.HasPrefix(v, "()") {
			env[k] = v
		}
	}
	for k, v := range inject {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil // SDK 默认客户端
	}
	return &http.Client{Transport: &headerTransport{base: http.DefaultTransport, headers: headers}}
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.base.RoundTrip(req)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefSlice(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// Manager 上游集合管理：按注册表中 gatewayMode && enabled 的 server 建立连接。
type Manager struct {
	mu        sync.Mutex
	getDefs   func() []*store.ServerDef
	upstreams map[string]*Upstream
}

func NewManager(getDefs func() []*store.ServerDef) *Manager {
	return &Manager{getDefs: getDefs, upstreams: map[string]*Upstream{}}
}

// AggregateDefs 聚合端点包含的上游。
func (m *Manager) AggregateDefs() []*store.ServerDef {
	out := []*store.ServerDef{}
	for _, d := range m.getDefs() {
		if d.GatewayMode && d.Enabled {
			out = append(out, d)
		}
	}
	return out
}

func (m *Manager) Has(id string) bool {
	for _, d := range m.getDefs() {
		if d.ID == id && d.Enabled {
			return true
		}
	}
	return false
}

// Get 取上游（懒创建；def 指针变化 = 配置更新 → 重建）。
func (m *Manager) Get(id string) *Upstream {
	for _, d := range m.getDefs() {
		if d.ID == id && d.Enabled {
			m.mu.Lock()
			defer m.mu.Unlock()
			u := m.upstreams[id]
			if u == nil || u.Def != d {
				u = newUpstream(d)
				m.upstreams[id] = u
			}
			return u
		}
	}
	return nil
}

func (m *Manager) Peek(id string) *Upstream {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.upstreams[id]
}

// StopAll 全部断开。
func (m *Manager) StopAll() {
	m.mu.Lock()
	all := make([]*Upstream, 0, len(m.upstreams))
	for _, u := range m.upstreams {
		all = append(all, u)
	}
	m.mu.Unlock()
	for _, u := range all {
		_ = u.Stop()
	}
}

// Snapshots 快照 = 已实例化的上游 + 启用中的网关型 server 的 idle 占位。
// 懒启动下未被调用过的 server 也要出现在列表里，管理页才能区分「启用」（配置态）与「未运行/出错」（运行态）。
func (m *Manager) Snapshots() []UpstreamSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]UpstreamSnapshot{}
	for _, d := range m.getDefs() {
		if !d.Enabled || !d.GatewayMode {
			continue
		}
		u, live := m.upstreams[d.ID]
		if live && u.Def == d {
			out[d.ID] = u.snapshot()
		} else {
			out[d.ID] = UpstreamSnapshot{ID: d.ID, Status: string(statusIdle)}
		}
	}
	for id, u := range m.upstreams {
		if _, ok := out[id]; !ok {
			out[id] = u.snapshot()
		}
	}
	list := make([]UpstreamSnapshot, 0, len(out))
	for _, s := range out {
		list = append(list, s)
	}
	return list
}

func (u *Upstream) snapshot() UpstreamSnapshot {
	status, lastError, pid, toolCount := u.snapshotStatus()
	return UpstreamSnapshot{
		ID:        u.Def.ID,
		Status:    string(status),
		PID:       pid,
		ToolCount: toolCount,
		LastError: lastError,
	}
}
