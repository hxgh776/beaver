package multica

import (
	"context"
	"strings"
)

// Agent Multica workspace 的 agent（面板绑定区的对象）。
type Agent struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Model  string `json:"model"`
}

// ListAgents 枚举 workspace 的 agent。
func (c *CLI) ListAgents(ctx context.Context) ([]Agent, error) {
	raw, err := c.runRaw(ctx, ListTimeout, "agent", "list", "--output", "json")
	if err != nil {
		return nil, err
	}
	items, err := unwrapArray(raw, "agents", "items", "data")
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(items))
	for _, it := range items {
		a := Agent{
			ID:     firstString(it, "id", "agent_id", "agentId"),
			Name:   firstString(it, "name", "display_name", "displayName"),
			Status: firstString(it, "status"),
			Model:  firstString(it, "model"),
		}
		if a.ID == "" {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// AgentMCPBinding agent 的 MCP 分配（enable/disable 只是停用，分配保留）。
type AgentMCPBinding struct {
	ServerID string `json:"serverId"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
}

// ListAgentMCPServerIDs 列出分配给某 agent 的 MCP 条目。
func (c *CLI) ListAgentMCPServerIDs(ctx context.Context, agentID string) ([]AgentMCPBinding, error) {
	if agentID == "" {
		return nil, &Error{Code: CodeInvalid, Message: "缺少 agent id"}
	}
	raw, err := c.runRaw(ctx, ListTimeout, "agent", "mcp", "list", agentID, "--output", "json")
	if err != nil {
		return nil, err
	}
	items, err := unwrapArray(raw, "servers", "bindings", "items", "data")
	if err != nil {
		return nil, err
	}
	out := make([]AgentMCPBinding, 0, len(items))
	for _, it := range items {
		b := AgentMCPBinding{
			ServerID: firstString(it, "server_id", "serverId", "id"),
			Name:     firstString(it, "name", "server_name", "serverName"),
			Enabled:  firstBool(it, "enabled", "is_enabled", "isEnabled"),
		}
		// enabled 缺省语义兜底：status 文本里若是 disabled 视为停用
		if st := firstString(it, "status"); st != "" && !itHasEnabledField(it) {
			b.Enabled = !strings.Contains(strings.ToLower(st), "disabled")
		}
		if b.ServerID == "" && b.Name == "" {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

func itHasEnabledField(it map[string]any) bool {
	for _, k := range []string{"enabled", "is_enabled", "isEnabled"} {
		if _, ok := it[k]; ok {
			return true
		}
	}
	return false
}

// BindAgentMCP 给 agent 分配一个库条目（默认启用；重复分配是幂等 no-op）。
func (c *CLI) BindAgentMCP(ctx context.Context, agentID, serverID string) error {
	if agentID == "" || serverID == "" {
		return &Error{Code: CodeInvalid, Message: "缺少 agent id 或 server id"}
	}
	_, err := c.run(ctx, WriteTimeout, "agent", "mcp", "add", agentID, serverID)
	return err
}

// UnbindAgentMCP 移除分配（库条目本身不受影响，其他 agent 保留自己的分配）。
func (c *CLI) UnbindAgentMCP(ctx context.Context, agentID, serverID string) error {
	if agentID == "" || serverID == "" {
		return &Error{Code: CodeInvalid, Message: "缺少 agent id 或 server id"}
	}
	_, err := c.run(ctx, WriteTimeout, "agent", "mcp", "remove", agentID, serverID)
	return err
}

// EnableAgentMCP 恢复启用一条分配。
func (c *CLI) EnableAgentMCP(ctx context.Context, agentID, serverID string) error {
	if agentID == "" || serverID == "" {
		return &Error{Code: CodeInvalid, Message: "缺少 agent id 或 server id"}
	}
	_, err := c.run(ctx, WriteTimeout, "agent", "mcp", "enable", agentID, serverID)
	return err
}

// DisableAgentMCP 停用分配（保留关系，便于以后一键恢复）。
func (c *CLI) DisableAgentMCP(ctx context.Context, agentID, serverID string) error {
	if agentID == "" || serverID == "" {
		return &Error{Code: CodeInvalid, Message: "缺少 agent id 或 server id"}
	}
	_, err := c.run(ctx, WriteTimeout, "agent", "mcp", "disable", agentID, serverID)
	return err
}

// 注意：这里刻意不封装 agent skills 的分配接口——beaver 不管理 Multica 平台内部
// 把技能分给哪些智能体（VENU-15 用户口径），技能只进 workspace 共享库。
