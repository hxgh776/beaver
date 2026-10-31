package gateway

// 测试夹具：一个最小的 stdio MCP server（re-exec 模式，本测试二进制自举）。
// 提供工具：echo / add / env。供网关集成测试使用（对齐 fixtures/fake-stdio-server.mjs）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestHelperFakeStdio 子进程入口：父测试以 os.Executable() + 环境变量重入本文件启动 stdio server。
func TestHelperFakeStdio(t *testing.T) {
	if os.Getenv("BEAVER_FAKE_STDIO") != "1" {
		return // 非子进程模式直接跳过
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-stdio-server", Version: "1.0.0"},
		&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}})
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				return &mcp.ListToolsResult{Tools: []*mcp.Tool{
					{
						Name:        "echo",
						Description: "Echo the message back",
						InputSchema: map[string]any{
							"type":       "object",
							"properties": map[string]any{"message": map[string]any{"type": "string"}},
							"required":   []string{"message"},
						},
					},
					{
						Name:        "add",
						Description: "Add two numbers",
						InputSchema: map[string]any{
							"type":       "object",
							"properties": map[string]any{"a": map[string]any{"type": "number"}, "b": map[string]any{"type": "number"}},
							"required":   []string{"a", "b"},
						},
					},
					{
						Name:        "env",
						Description: "Return the MAGIC_VALUE env var (credential pass-through check)",
						InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
					},
				}}, nil
			case "tools/call":
				params := req.(*mcp.ServerRequest[*mcp.CallToolParamsRaw]).Params
				var args struct {
					Message string  `json:"message"`
					A       float64 `json:"a"`
					B       float64 `json:"b"`
				}
				_ = json.Unmarshal(params.Arguments, &args)
				switch params.Name {
				case "echo":
					return &mcp.CallToolResult{Content: []mcp.Content{
						&mcp.TextContent{Text: fmt.Sprintf("echo: %s", args.Message)},
					}}, nil
				case "add":
					return &mcp.CallToolResult{Content: []mcp.Content{
						&mcp.TextContent{Text: fmt.Sprintf("%g", args.A+args.B)},
					}}, nil
				case "env":
					return &mcp.CallToolResult{Content: []mcp.Content{
						&mcp.TextContent{Text: os.Getenv("MAGIC_VALUE")},
					}}, nil
				default:
					return nil, fmt.Errorf("unknown tool: %s", params.Name)
				}
			}
			return next(ctx, method, req)
		}
	})
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}
