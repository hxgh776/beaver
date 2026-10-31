package gateway

// 网关（模块 2 核心）：借宿 daemon 的 HTTP 端口，激活 /mcp 与 /servers/:id/mcp 与 /agents/:type/mcp。
// start/stop 只是挂载/卸载 HTTP 钩子并断开上游，网关本身不占独立端口。
// 对应 TS packages/server/src/gateway/gateway.ts。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hxgh776/beaver/internal/daemon"
	"github.com/hxgh776/beaver/internal/registry"
	"github.com/hxgh776/beaver/internal/store"
)

const (
	callsLogMaxMemory = 500
	callsTailBytes    = 256 * 1024
	callsTailRecords  = 200
)

var (
	perServerRe = regexp.MustCompile(`^/servers/([^/]+)/mcp$`)
	perAgentRe  = regexp.MustCompile(`^/agents/([^/]+)/mcp$`)
)

// Gateway MCP 网关。
type Gateway struct {
	daemon *daemon.Daemon
	mgr    *Manager

	mu         sync.Mutex
	running    bool
	toolIdx    map[string]map[string]ToolIndexEntry
	recent     []CallRecord
	callsMu    sync.Mutex
	callsLogMu sync.Mutex // ndjson 追加写串行化
}

// New 创建网关（尚未挂载）。
func New(d *daemon.Daemon) *Gateway {
	da := d
	return &Gateway{
		daemon:  da,
		mgr:     NewManager(func() []*store.ServerDef { return da.Config.Servers }),
		toolIdx: map[string]map[string]ToolIndexEntry{},
	}
}

// Attach 挂载到 daemon（幂等）：注入全部网关钩子。
func (g *Gateway) Attach() {
	g.mu.Lock()
	if g.running {
		g.mu.Unlock()
		return
	}
	g.running = true
	g.mu.Unlock()

	d := g.daemon
	d.GatewayHTTP = g.httpHandler()
	d.GatewayControl = func(action string) (daemon.GatewayStatus, error) {
		if action == "start" {
			g.Attach()
		} else {
			_ = g.Detach()
		}
		return g.Status(), nil
	}
	d.GatewayStatusFn = g.Status
	d.GatewaySnapshots = func() any { return g.mgr.Snapshots() }
	d.UpstreamControl = func(id string, action string) (daemon.UpstreamControlResult, error) {
		u := g.mgr.Get(id)
		if u == nil {
			return daemon.UpstreamControlResult{}, fmt.Errorf("上游不存在或未启用: %s", id)
		}
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		switch action {
		case "stop":
			_ = u.Stop()
		case "start":
			if _, err := u.EnsureClient(ctx); err != nil {
				return daemon.UpstreamControlResult{}, err
			}
		default: // restart
			_ = u.Stop()
			if _, err := u.EnsureClient(ctx); err != nil {
				return daemon.UpstreamControlResult{}, err
			}
		}
		state := string(statusIdle)
		for _, s := range g.mgr.Snapshots() {
			if s.ID == id {
				state = s.Status
				break
			}
		}
		return daemon.UpstreamControlResult{Ok: true, State: state}, nil
	}
	d.ProbeTools = func(id string, refresh bool) []daemon.ToolInfo {
		u := g.mgr.Get(id)
		if u == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		// 缓存优先：工具列表抓取慢（需拉起上游进程），已抓过直接返回；refresh=1 才强制重抓
		tools, err := u.ListTools(ctx, !refresh)
		if err != nil {
			return nil
		}
		out := make([]daemon.ToolInfo, 0, len(tools))
		for _, t := range tools {
			info := daemon.ToolInfo{Name: t.Name, Enabled: true}
			if t.Description != "" {
				desc := t.Description
				info.Description = &desc
			}
			if t.Name != "" {
				info.Enabled = isToolEnabledFor(g.daemon, id, t.Name)
			}
			out = append(out, info)
		}
		return out
	}
	d.UpstreamLogs = func(id string) []string {
		if u := g.mgr.Peek(id); u != nil {
			return u.StderrLines()
		}
		return []string{}
	}
	// Issue-3：从 ndjson 尾部读取，daemon 重启后日志不丢；文件不可读时回退内存环
	d.RecentCalls = func() any { return g.ReadRecentCallsFromDisk() }
	// M3.1 调试台（S6）
	d.InvokeTool = g.invokeTool
	d.Logger.Info("gateway attached")
}

// Detach 卸载并断开全部上游。
func (g *Gateway) Detach() error {
	g.mu.Lock()
	g.running = false
	g.mu.Unlock()
	g.daemon.GatewayHTTP = nil
	g.mgr.StopAll()
	g.daemon.Logger.Info("gateway detached")
	return nil
}

// Status 网关运行态。
func (g *Gateway) Status() daemon.GatewayStatus {
	g.mu.Lock()
	running := g.running
	g.mu.Unlock()
	if running {
		port := g.daemon.ActualPort()
		return daemon.GatewayStatus{Running: true, Port: &port}
	}
	return daemon.GatewayStatus{Running: false}
}

// UpstreamSnapshots 上游运行态列表。
func (g *Gateway) UpstreamSnapshots() []UpstreamSnapshot { return g.mgr.Snapshots() }

// invokeTool M3.1 调试台：直接调用某 server 的某工具。
func (g *Gateway) invokeTool(id string, tool string, args map[string]any) (any, error) {
	u := g.mgr.Get(id)
	if u == nil {
		return nil, fmt.Errorf("上游不存在或未启用: %s", id)
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	result, err := u.CallTool(ctx, tool, raw)
	if err != nil {
		msg := err.Error()
		g.recordCall(CallRecord{
			TS: nowISO(), Scope: "debug", ServerID: id, Tool: tool,
			OK: false, DurationMs: time.Since(started).Milliseconds(), Error: &msg,
		})
		return nil, err
	}
	var errMsg *string
	if result.IsError {
		s := "上游返回错误"
		errMsg = &s
	}
	g.recordCall(CallRecord{
		TS: nowISO(), Scope: "debug", ServerID: id, Tool: tool,
		OK: !result.IsError, DurationMs: time.Since(started).Milliseconds(), Error: errMsg,
	})
	return result, nil
}

func isToolEnabledFor(d *daemon.Daemon, serverID, toolName string) bool {
	for _, s := range d.Config.Servers {
		if s.ID == serverID {
			return IsToolEnabled(s, toolName)
		}
	}
	return false
}

// boundIDs agent 的有效绑定（直连 ∪ 分组）。
func (g *Gateway) boundIDs(agentType string) map[string]struct{} {
	return registry.EffectiveBoundIDs(g.daemon.Config, agentType)
}

func (g *Gateway) toolIndexFor(scope string) map[string]ToolIndexEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	idx, ok := g.toolIdx[scope]
	if !ok {
		idx = map[string]ToolIndexEntry{}
		g.toolIdx[scope] = idx
	}
	return idx
}

func (g *Gateway) setToolIndex(scope, exposed string, u *Upstream, originalName string) {
	idx := g.toolIndexFor(scope)
	g.mu.Lock()
	defer g.mu.Unlock()
	idx[exposed] = ToolIndexEntry{Upstream: u, OriginalName: originalName}
}

func (g *Gateway) lookupToolIndex(scope, exposed string) (*ToolIndexEntry, bool) {
	idx := g.toolIndexFor(scope)
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := idx[exposed]
	if !ok {
		return nil, false
	}
	return &entry, true
}

func (g *Gateway) recordCall(record CallRecord) {
	g.callsMu.Lock()
	g.recent = append(g.recent, record)
	if len(g.recent) > callsLogMaxMemory {
		g.recent = g.recent[len(g.recent)-callsLogMaxMemory:]
	}
	g.callsMu.Unlock()
	g.callsLogMu.Lock()
	defer g.callsLogMu.Unlock()
	f, err := os.OpenFile(g.callsLogFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
}

func (g *Gateway) callsLogFile() string {
	return filepath.Join(g.daemon.Store.LogsDir(), "gateway-calls.ndjson")
}

// ReadRecentCallsFromDisk 读取 ndjson 尾部（最多 256KB / 200 条），跳过损坏行。
func (g *Gateway) ReadRecentCallsFromDisk() []CallRecord {
	g.callsMu.Lock()
	fallback := g.recent
	g.callsMu.Unlock()
	data, err := os.ReadFile(g.callsLogFile())
	if err != nil || len(data) == 0 {
		return fallback
	}
	// 只保留尾部（简化：文件读入后取尾部 256KB，避免 stat+seek 的平台差异）
	if len(data) > callsTailBytes {
		data = data[len(data)-callsTailBytes:]
		// 首行可能被截断
		if idx := strings.IndexByte(string(data), '\n'); idx >= 0 {
			data = data[idx+1:]
		}
	}
	lines := strings.Split(string(data), "\n")
	out := []CallRecord{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r CallRecord
		if err := json.Unmarshal([]byte(line), &r); err == nil {
			out = append(out, r)
		}
	}
	if len(out) > callsTailRecords {
		out = out[len(out)-callsTailRecords:]
	}
	return out
}

// httpHandler 返回挂载到 daemon 的 HTTP 处理钩子。
func (g *Gateway) httpHandler() daemon.GatewayHTTPHook {
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		upstreamID, agentScope := "", ""
		if v := r.Context().Value(ctxKeyUpstream{}); v != nil {
			upstreamID = v.(string)
		}
		if v := r.Context().Value(ctxKeyAgent{}); v != nil {
			agentScope = v.(string)
		}
		return g.buildServer(upstreamID, agentScope)
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true, // 规避各 agent 会话行为差异（§1.5-C）
		JSONResponse: true,
	})

	return func(w http.ResponseWriter, r *http.Request, pathname string, query url.Values) bool {
		g.mu.Lock()
		running := g.running
		g.mu.Unlock()
		if !running {
			sendGatewayError(w, 503, "gateway 未启动", "GATEWAY_DOWN")
			return true
		}
		// D7：仅当访问令牌开启时校验；关闭时依赖环回监听兜底
		if authRequired(g.daemon) && !daemon.CheckToken(r, g.daemon.Config.Settings.Token, query) {
			sendGatewayError(w, 401, "未授权", "UNAUTHORIZED")
			return true
		}

		var upstreamID, agentScope string
		if pathname != "/mcp" {
			if m := perServerRe.FindStringSubmatch(pathname); m != nil {
				if id, err := url.PathUnescape(m[1]); err == nil {
					upstreamID = id
				} else {
					upstreamID = m[1]
				}
				if !g.mgr.Has(upstreamID) {
					sendGatewayError(w, 404, fmt.Sprintf("上游不存在或未启用: %s", upstreamID), "")
					return true
				}
			} else if m := perAgentRe.FindStringSubmatch(pathname); m != nil {
				if id, err := url.PathUnescape(m[1]); err == nil {
					agentScope = id
				} else {
					agentScope = m[1]
				}
			} else {
				sendGatewayError(w, 404, "not found", "")
				return true
			}
		}

		ctx := r.Context()
		if upstreamID != "" {
			ctx = context.WithValue(ctx, ctxKeyUpstream{}, upstreamID)
		}
		if agentScope != "" {
			ctx = context.WithValue(ctx, ctxKeyAgent{}, agentScope)
		}
		handler.ServeHTTP(w, r.WithContext(ctx))
		return true
	}
}

type ctxKeyUpstream struct{}
type ctxKeyAgent struct{}

func authRequired(d *daemon.Daemon) bool {
	return d.Config.Settings.AuthRequired != nil && *d.Config.Settings.AuthRequired
}

func sendGatewayError(w http.ResponseWriter, status int, message, code string) {
	payload := map[string]any{"error": message}
	if code != "" {
		payload["code"] = code
	}
	body, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.Copy(w, strings.NewReader(string(body)))
}
