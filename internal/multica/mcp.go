package multica

import (
	"context"
	"encoding/json"
	"fmt"
)

// MCPServer workspace MCP 库条目（Multica 的 list 是刻意脱敏的：只回 name/transport 等
// 元数据，配置本体 write-only 永不返回——含 token 的配置对任何人都不可读）。
type MCPServer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Transport string `json:"transport"`
}

// ListMCPServers 列出 workspace MCP 库。解析容忍形状差异：顶层数组或
// {"servers":[...]} 包装；字段兼容 camelCase/snake_case。
func (c *CLI) ListMCPServers(ctx context.Context) ([]MCPServer, error) {
	var raw json.RawMessage
	if err := c.runJSON(ctx, ListTimeout, &raw, "workspace", "mcp", "list", "--output", "json"); err != nil {
		return nil, err
	}
	items, err := unwrapArray(raw, "servers", "items", "data")
	if err != nil {
		return nil, err
	}
	out := make([]MCPServer, 0, len(items))
	for i, it := range items {
		s := MCPServer{
			ID:        firstString(it, "id", "server_id", "serverId"),
			Name:      firstString(it, "name", "server_name", "serverName"),
			Transport: firstString(it, "transport", "transport_type", "transportType", "type"),
		}
		if s.ID == "" && s.Name == "" {
			continue // 无法识别的行：跳过而不是让整个列表失败
		}
		if s.ID == "" {
			s.ID = fmt.Sprintf("unnamed-%d", i)
		}
		out = append(out, s)
	}
	return out, nil
}

// AddMCPServer 新增库条目。configJSON 是 mcpServers 下同一形状的对象
//（http: {url, headers?}；stdio: {command, args?, env?}）。凭据经临时文件传递，
// 不进命令行、不落 config.json。
func (c *CLI) AddMCPServer(ctx context.Context, name, configJSON string) error {
	if name == "" {
		return &Error{Code: CodeInvalid, Message: "MCP 条目名称不能为空"}
	}
	if err := validateConfigJSON(configJSON); err != nil {
		return err
	}
	path, cleanup, err := c.tempConfigFile(configJSON, "beaver-mcp-*.json")
	if err != nil {
		return err
	}
	defer cleanup()
	_, err = c.run(ctx, WriteTimeout, "workspace", "mcp", "add", name, "--server-config-file", path)
	return err
}

// UpdateMCPServer 改名 / 替换配置（配置为空串 = 只改名）。Multica 配置本体不可读
// （write-only），替换需要提供完整新配置。
func (c *CLI) UpdateMCPServer(ctx context.Context, id, newName, configJSON string) error {
	if id == "" {
		return &Error{Code: CodeInvalid, Message: "缺少 server id"}
	}
	args := []string{"workspace", "mcp", "update", id}
	if newName != "" {
		args = append(args, "--name", newName)
	}
	if configJSON != "" {
		if err := validateConfigJSON(configJSON); err != nil {
			return err
		}
		path, cleanup, err := c.tempConfigFile(configJSON, "beaver-mcp-*.json")
		if err != nil {
			return err
		}
		defer cleanup()
		args = append(args, "--server-config-file", path)
	}
	if len(args) == 4 {
		return &Error{Code: CodeInvalid, Message: "更新至少需要 name 或新的配置"}
	}
	_, err := c.run(ctx, WriteTimeout, args...)
	return err
}

// RemoveMCPServer 从库删除条目（Multica 语义：同时解除所有 agent 的分配）。
func (c *CLI) RemoveMCPServer(ctx context.Context, id string) error {
	if id == "" {
		return &Error{Code: CodeInvalid, Message: "缺少 server id"}
	}
	_, err := c.run(ctx, WriteTimeout, "workspace", "mcp", "remove", id)
	return err
}
