package gateway

// G7 stdio 反向桥：把一个 streamable HTTP 上游暴露为本地 stdio MCP server。
// 让只支持 stdio 的 agent（如 Codex）用上远程 HTTP server 与网关聚合端点。
// 对应 TS packages/server/src/gateway/bridge.ts + bridge-main.ts。
// 独立于 daemon 运行（纯透传、无本地状态）；由同步引擎写为
// `beaver bridge <url> --token=... [--header k=v ...]`。

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// BridgeOptions 反向桥参数。
type BridgeOptions struct {
	URL     string
	Headers map[string]string
	Name    string
}

// ParseBridgeArgs 解析 CLI 参数：url --token=xxx --header k=v --name xxx。
func ParseBridgeArgs(argv []string) (BridgeOptions, error) {
	var opts BridgeOptions
	headers := map[string]string{}
	hasURL := false
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--header" && i+1 < len(argv):
			kv := argv[i+1]
			if idx := strings.Index(kv, "="); idx > 0 {
				headers[kv[:idx]] = kv[idx+1:]
			}
			i++
		case strings.HasPrefix(arg, "--header="):
			kv := arg[len("--header="):]
			if idx := strings.Index(kv, "="); idx > 0 {
				headers[kv[:idx]] = kv[idx+1:]
			}
		case arg == "--token" && i+1 < len(argv):
			headers["Authorization"] = "Bearer " + argv[i+1]
			i++
		case strings.HasPrefix(arg, "--token="):
			headers["Authorization"] = "Bearer " + arg[len("--token="):]
		case arg == "--name" && i+1 < len(argv):
			opts.Name = argv[i+1]
			i++
		case strings.HasPrefix(arg, "--name="):
			opts.Name = arg[len("--name="):]
		case !strings.HasPrefix(arg, "--") && !hasURL:
			opts.URL = arg
			hasURL = true
		default:
			return opts, fmt.Errorf("未知参数: %s", arg)
		}
	}
	if !hasURL {
		return opts, fmt.Errorf("用法: bridge <url> [--token=xxx] [--header k=v ...] [--name xxx]")
	}
	if len(headers) > 0 {
		opts.Headers = headers
	}
	return opts, nil
}

// bridgeConn 懒建连的上游客户端（单连接复用；断开后重连）。
type bridgeConn struct {
	opts BridgeOptions

	mu     sync.Mutex
	cond   *sync.Cond
	sess   *mcp.ClientSession
	busy   bool
	lastEr error
}

func newBridgeConn(opts BridgeOptions) *bridgeConn {
	c := &bridgeConn{opts: opts}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (b *bridgeConn) upstream(ctx context.Context) (*mcp.ClientSession, error) {
	b.mu.Lock()
	for b.busy && b.sess == nil && b.lastEr == nil {
		b.cond.Wait() // 首连进行中，等待结果
	}
	if b.sess != nil {
		s := b.sess
		b.mu.Unlock()
		return s, nil
	}
	if b.busy { // 上一位连接失败
		err := b.lastEr
		b.busy = false
		b.lastEr = nil
		b.mu.Unlock()
		return nil, err
	}
	b.busy = true
	b.mu.Unlock()

	s, err := b.connect(ctx)
	b.mu.Lock()
	b.busy = false
	if err != nil {
		b.lastEr = err
		b.cond.Broadcast()
		b.mu.Unlock()
		return nil, err
	}
	b.lastEr = nil
	b.sess = s
	b.cond.Broadcast()
	b.mu.Unlock()
	go func() {
		s.Wait() // 上游断开 → 允许下次调用重连
		b.mu.Lock()
		if b.sess == s {
			b.sess = nil
		}
		b.mu.Unlock()
	}()
	return s, nil
}

func (b *bridgeConn) connect(ctx context.Context) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "beaver-bridge", Version: "0.1.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:             b.opts.URL,
		HTTPClient:           httpClientWithHeaders(b.opts.Headers),
		DisableStandaloneSSE: true,
	}
	return client.Connect(ctx, transport, nil)
}

// createBridgeServer 构造桥 server（stdio 侧）。
func createBridgeServer(opts BridgeOptions) *mcp.Server {
	conn := newBridgeConn(opts)
	name := opts.Name
	if name == "" {
		name = "beaver-bridge"
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: name, Version: "0.1.0"},
		&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}})
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "ping":
				// ping 不触发建连（TS 版先 upstream 再 ping；这里保持一致）
				s, err := conn.upstream(ctx)
				if err != nil {
					return nil, err
				}
				if err := s.Ping(ctx, nil); err != nil {
					return nil, err
				}
				return next(ctx, method, req)
			case "tools/list":
				s, err := conn.upstream(ctx)
				if err != nil {
					return nil, err
				}
				// 翻页在此处追平（stdio agent 按无 cursor 终止即可）
				var tools []*mcp.Tool
				cursor := ""
				for {
					params := &mcp.ListToolsParams{}
					if cursor != "" {
						params.Cursor = cursor
					}
					res, err := s.ListTools(ctx, params)
					if err != nil {
						return nil, err
					}
					tools = append(tools, res.Tools...)
					if res.NextCursor == "" {
						break
					}
					cursor = res.NextCursor
				}
				return &mcp.ListToolsResult{Tools: tools}, nil
			case "tools/call":
				s, err := conn.upstream(ctx)
				if err != nil {
					return nil, err
				}
				params := req.(*mcp.ServerRequest[*mcp.CallToolParamsRaw]).Params
				return s.CallTool(ctx, &mcp.CallToolParams{Name: params.Name, Arguments: argsRaw(params.Arguments)})
			}
			return next(ctx, method, req)
		}
	})
	return srv
}

// RunBridgeStdio 独立入口：连接 stdio 并服务直到 stdin 关闭。
func RunBridgeStdio(opts BridgeOptions) error {
	server := createBridgeServer(opts)
	fmt.Fprintf(os.Stderr, "[dam bridge] → %s\n", opts.URL)
	return server.Run(context.Background(), &mcp.StdioTransport{})
}

var _ = http.DefaultTransport // headerTransport 在 upstream.go 定义，跨文件复用
