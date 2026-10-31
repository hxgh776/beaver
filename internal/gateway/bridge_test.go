package gateway

// G7 桥测试：进程内 bridge server ↔ 真实 streamable HTTP 上游（含鉴权头透传检查）。
// 对应 TS packages/server/test/bridge.test.ts；stdio 侧用 InMemoryTransport 替代真实子进程。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParseBridgeArgs(t *testing.T) {
	opts, err := ParseBridgeArgs([]string{"http://x/mcp", "--token=tok", "--header", "a=b", "--name=bridged"})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if opts.URL != "http://x/mcp" || opts.Name != "bridged" {
		t.Fatalf("字段不符: %+v", opts)
	}
	if opts.Headers["Authorization"] != "Bearer tok" || opts.Headers["a"] != "b" {
		t.Fatalf("headers 不符: %v", opts.Headers)
	}

	if _, err := ParseBridgeArgs([]string{}); err == nil {
		t.Fatalf("缺 url 应报错")
	}
}

// startAuthCheckingUpstream fake HTTP 上游；requireAuth 时校验 Authorization 并回显到工具文本。
func startAuthCheckingUpstream(t *testing.T, requireAuth bool) (*string, *authSeenBox) {
	t.Helper()
	box := &authSeenBox{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-http-upstream", Version: "1.0.0"},
		&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}})
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				return &mcp.ListToolsResult{Tools: []*mcp.Tool{{
					Name: "http_tool", Description: "from http upstream",
					InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
				}}}, nil
			case "tools/call":
				params := req.(*mcp.ServerRequest[*mcp.CallToolParamsRaw]).Params
				return &mcp.CallToolResult{Content: []mcp.Content{
					&mcp.TextContent{Text: "http:" + params.Name},
				}}, nil
			}
			return next(ctx, method, req)
		}
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireAuth {
			box.set(r.Header.Get("Authorization"))
			if r.Header.Get("Authorization") != "Bearer bridge-test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
		}
		inner.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return strp(ts.URL + "/mcp"), box
}

type authSeenBox struct {
	mu sync.Mutex
	v  string
}

func (b *authSeenBox) set(v string) {
	b.mu.Lock()
	b.v = v
	b.mu.Unlock()
}

func (b *authSeenBox) get() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}

// 桥 server（经 InMemoryTransport 挂到客户端）把 HTTP 上游暴露出来：listTools + callTool 透传
func TestBridgePassthrough(t *testing.T) {
	upstreamURL, _ := startAuthCheckingUpstream(t, false)
	bridge := createBridgeServer(BridgeOptions{URL: *upstreamURL, Name: "bridged"})

	client := mcp.NewClient(&mcp.Implementation{Name: "bridge-test", Version: "1.0.0"}, nil)
	srvTransport, cliTransport := mcp.NewInMemoryTransports()
	if _, err := bridge.Connect(context.Background(), srvTransport, nil); err != nil {
		t.Fatalf("桥 server 连接失败: %v", err)
	}
	sess, err := client.Connect(context.Background(), cliTransport, nil)
	if err != nil {
		t.Fatalf("客户端连接失败: %v", err)
	}
	defer sess.Close()

	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("listTools 失败: %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "http_tool" {
		t.Fatalf("工具列表不符: %v", tools.Tools)
	}
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "http_tool", Arguments: map[string]any{"k": 1}})
	if err != nil {
		t.Fatalf("callTool 失败: %v", err)
	}
	if text := textOf(res); !strings.HasPrefix(text, "http:http_tool") {
		t.Fatalf("返回不符: %q", text)
	}
}

// --token 透传为 Authorization: Bearer
func TestBridgeTokenHeaderPassthrough(t *testing.T) {
	upstreamURL, box := startAuthCheckingUpstream(t, true)
	bridge := createBridgeServer(BridgeOptions{
		URL:     *upstreamURL,
		Headers: map[string]string{"Authorization": "Bearer bridge-test-token"},
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "bridge-test", Version: "1.0.0"}, nil)
	srvTransport, cliTransport := mcp.NewInMemoryTransports()
	if _, err := bridge.Connect(context.Background(), srvTransport, nil); err != nil {
		t.Fatalf("桥 server 连接失败: %v", err)
	}
	sess, err := client.Connect(context.Background(), cliTransport, nil)
	if err != nil {
		t.Fatalf("客户端连接失败: %v", err)
	}
	defer sess.Close()

	if _, err := sess.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("带 token listTools 失败: %v", err)
	}
	if box.get() != "Bearer bridge-test-token" {
		t.Fatalf("上游应看到 Bearer 头，得到 %q", box.get())
	}
}
