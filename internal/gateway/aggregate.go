package gateway

// 聚合 MCP server（G1/G3/G4/G5/G13）与 per-server 独立端点（G6）。
// 对应 TS packages/server/src/gateway/aggregate.ts。
// 实现方式：go-sdk 的 Server 不预注册任何工具，用 AddReceivingMiddleware 拦截
// tools/list / tools/call，其余方法（initialize/ping/notifications）交回默认处理器。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hxgh776/beaver/internal/store"
)

// CallRecord 调用日志条目（ndjson 落盘 + 内存环）。
type CallRecord struct {
	TS         string  `json:"ts"`
	Scope      string  `json:"scope"`
	ServerID   string  `json:"serverId"`
	Tool       string  `json:"tool"`
	OK         bool    `json:"ok"`
	DurationMs int64   `json:"durationMs"`
	Error      *string `json:"error,omitempty"`
}

// ToolIndexEntry 暴露名 → 上游原始工具的映射（调用路由依据）。
type ToolIndexEntry struct {
	Upstream     *Upstream
	OriginalName string
}

// IsToolEnabled 工具开关：toolOverrides[name].enabled !== false。
func IsToolEnabled(def *store.ServerDef, toolName string) bool {
	if def.ToolOverrides == nil {
		return true
	}
	if ov, ok := (*def.ToolOverrides)[toolName]; ok {
		return ov.Enabled
	}
	return true
}

// buildServer 构造一个 MCP server 实例（每次 HTTP 请求新建，TS 同款 stateless 模式）。
func (g *Gateway) buildServer(upstreamID, agentScope string) *mcp.Server {
	impl := &mcp.Implementation{Name: "beaver", Version: "0.1.0"}
	if upstreamID != "" {
		impl.Name = "beaver:" + upstreamID
	}
	srv := mcp.NewServer(impl, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	srv.AddReceivingMiddleware(g.middlewareFor(upstreamID, agentScope))
	return srv
}

// middlewareFor 按端点作用域生成请求拦截器：
//   - upstreamID 非空：per-server 独立端点，工具名保持原始形态；
//   - agentScope 非空：per-agent 分组端点，只含该 agent 的有效绑定；
//   - 两者皆空：聚合端点。
func (g *Gateway) middlewareFor(upstreamID, agentScope string) mcp.Middleware {
	scope := "aggregate"
	if upstreamID != "" {
		scope = "single:" + upstreamID
	} else if agentScope != "" {
		scope = "agent:" + agentScope
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				if upstreamID != "" {
					return g.handleSingleListTools(ctx, upstreamID)
				}
				return g.handleAggregateListTools(ctx, scope, agentScope)
			case "tools/call":
				params := req.(*mcp.ServerRequest[*mcp.CallToolParamsRaw]).Params
				if upstreamID != "" {
					return g.handleSingleCallTool(ctx, upstreamID, scope, params)
				}
				return g.handleAggregateCallTool(ctx, scope, params)
			}
			return next(ctx, method, req)
		}
	}
}

// handleAggregateListTools tools/list：合并所有就绪上游（可按 agent 过滤）；
// 冲突工具名加 `serverId__` 前缀；被停用的工具直接剔除（上下文成本控制）。
func (g *Gateway) handleAggregateListTools(ctx context.Context, scope, agentScope string) (mcp.Result, error) {
	var bound map[string]struct{}
	if agentScope != "" {
		bound = g.boundIDs(agentScope)
	}
	type result struct {
		up    *Upstream
		tools []*mcp.Tool
		err   error
	}
	// 过滤在抓取之前：per-agent 端点不该为未绑定的上游拉起进程
	defs := []*store.ServerDef{}
	for _, d := range g.mgr.AggregateDefs() {
		if bound == nil {
			defs = append(defs, d)
			continue
		}
		if _, keep := bound[d.ID]; keep {
			defs = append(defs, d)
		}
	}
	results := make([]result, len(defs))
	done := make(chan int, len(defs))
	// 并发抓取（Promise.allSettled 等价：单上游失败不影响整体）
	for i, d := range defs {
		u := g.mgr.Get(d.ID)
		if u == nil {
			results[i] = result{err: fmt.Errorf("上游不存在: %s", d.ID)}
			done <- i
			continue
		}
		go func(i int, u *Upstream) {
			tools, err := u.ListTools(ctx, false)
			results[i] = result{up: u, tools: tools, err: err}
			done <- i
		}(i, u)
	}
	for range defs {
		<-done
	}

	nameCounts := map[string]int{}
	collected := []result{}
	for _, r := range results {
		if r.err != nil {
			continue // 单上游失败不影响整体
		}
		enabled := []*mcp.Tool{}
		for _, t := range r.tools {
			if IsToolEnabled(r.up.Def, t.Name) {
				enabled = append(enabled, t)
				nameCounts[t.Name]++
			}
		}
		collected = append(collected, result{up: r.up, tools: enabled})
	}

	// 稳定排序：按 defs 顺序输出（JS 数组天然有序，这里显式按 defs 顺序收集）
	defOrder := map[string]int{}
	for i, d := range defs {
		defOrder[d.ID] = i
	}
	sort.SliceStable(collected, func(a, b int) bool {
		return defOrder[collected[a].up.Def.ID] < defOrder[collected[b].up.Def.ID]
	})

	tools := []*mcp.Tool{}
	for _, r := range collected {
		for _, t := range r.tools {
			exposed := t.Name
			if nameCounts[t.Name] > 1 {
				exposed = r.up.Def.ID + "__" + t.Name
			}
			g.setToolIndex(scope, exposed, r.up, t.Name)
			nt := *t
			nt.Name = exposed
			tools = append(tools, &nt)
		}
	}
	return &mcp.ListToolsResult{Tools: tools}, nil
}

// handleAggregateCallTool tools/call：经映射路由回上游；未知工具回退 `serverId__tool` 前缀解析。
func (g *Gateway) handleAggregateCallTool(ctx context.Context, scope string, params *mcp.CallToolParamsRaw) (mcp.Result, error) {
	started := time.Now()
	name := params.Name
	entry, _ := g.lookupToolIndex(scope, name)
	if entry == nil {
		// 前缀回退：serverId 不含下划线（slug），按第一个 __ 拆分
		for i := 0; i+1 < len(name); i++ {
			if name[i:i+2] == "__" && i > 0 {
				if u := g.mgr.Get(name[:i]); u != nil {
					entry = &ToolIndexEntry{Upstream: u, OriginalName: name[i+2:]}
				}
				break
			}
		}
	}
	if entry == nil {
		msg := fmt.Sprintf("未知工具: %s", name)
		g.recordCall(callRecordFor(scope, "unknown", name, started, false, &msg))
		return nil, fmt.Errorf("%s", msg)
	}
	if !IsToolEnabled(entry.Upstream.Def, entry.OriginalName) {
		msg := fmt.Sprintf("工具已停用: %s", name)
		g.recordCall(callRecordFor(scope, entry.Upstream.Def.ID, entry.OriginalName, started, false, &msg))
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("工具 %s 已被停用（beaver）", name)}},
			IsError: true,
		}, nil
	}
	res, err := entry.Upstream.CallTool(ctx, entry.OriginalName, argsRaw(params.Arguments).(json.RawMessage))
	if err != nil {
		msg := err.Error()
		g.recordCall(callRecordFor(scope, entry.Upstream.Def.ID, entry.OriginalName, started, false, &msg))
		return nil, err
	}
	var errMsg *string
	if res.IsError {
		s := "上游返回错误"
		errMsg = &s
	}
	g.recordCall(callRecordFor(scope, entry.Upstream.Def.ID, entry.OriginalName, started, !res.IsError, errMsg))
	return res, nil
}

// handleSingleListTools per-server 独立端点：薄反代，工具名保持原始形态。
func (g *Gateway) handleSingleListTools(ctx context.Context, upstreamID string) (mcp.Result, error) {
	u := g.mgr.Get(upstreamID)
	if u == nil {
		return nil, fmt.Errorf("上游不存在: %s", upstreamID)
	}
	tools, err := u.ListTools(ctx, false)
	if err != nil {
		return nil, err
	}
	out := []*mcp.Tool{}
	for _, t := range tools {
		if IsToolEnabled(u.Def, t.Name) {
			out = append(out, t)
		}
	}
	return &mcp.ListToolsResult{Tools: out}, nil
}

// handleSingleCallTool per-server 独立端点调用。
func (g *Gateway) handleSingleCallTool(ctx context.Context, upstreamID, scope string, params *mcp.CallToolParamsRaw) (mcp.Result, error) {
	u := g.mgr.Get(upstreamID)
	if u == nil {
		return nil, fmt.Errorf("上游不存在: %s", upstreamID)
	}
	started := time.Now()
	if !IsToolEnabled(u.Def, params.Name) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("工具 %s 已被停用（beaver）", params.Name)}},
			IsError: true,
		}, nil
	}
	res, err := u.CallTool(ctx, params.Name, argsRaw(params.Arguments).(json.RawMessage))
	if err != nil {
		msg := err.Error()
		g.recordCall(callRecordFor(scope, upstreamID, params.Name, started, false, &msg))
		return nil, err
	}
	var errMsg *string
	if res.IsError {
		s := "上游返回错误"
		errMsg = &s
	}
	g.recordCall(callRecordFor(scope, upstreamID, params.Name, started, !res.IsError, errMsg))
	return res, nil
}

func callRecordFor(scope, serverID, tool string, started time.Time, ok bool, errMsg *string) CallRecord {
	return CallRecord{
		TS:         nowISO(),
		Scope:      scope,
		ServerID:   serverID,
		Tool:       tool,
		OK:         ok,
		DurationMs: time.Since(started).Milliseconds(),
		Error:      errMsg,
	}
}

// argsRaw 上游客户端 Arguments 期望 any；raw JSON 直接传（SDK 会再 Marshal）。
func argsRaw(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	return raw
}

func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
