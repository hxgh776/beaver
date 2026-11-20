// Package multica 把 multica CLI 当作 API 契约封装成 beaver 的外部后端适配层（VENU-15）。
//
// 身份边界：beaver 不持有 Multica token，登录态完全复用本机 multica CLI 的 profile；
// 凭据边界：MCP 条目（常含 token）只经内存 → 临时文件（--server-config-file）→ CLI 推送，
// 用后即焚，绝不写进 beaver 的 config.json，命令行也不内联凭据；
// 失败语义：所有失败归类为可诊断的 *Error（code + hint），由面板展示修复建议。
package multica

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// 默认超时：探测 15s / 列表 30s / 写操作 60s / 导入（要下载归档）180s。
const (
	ProbeTimeout  = 15 * time.Second
	ListTimeout   = 30 * time.Second
	WriteTimeout  = 60 * time.Second
	ImportTimeout = 180 * time.Second
)

// CLI 一次 multica 调用的配置。字段全部来自 beaver settings.multica（或缺省），
// 每次请求按当前配置新建，不跨请求缓存。
type CLI struct {
	// Binary multica 可执行文件；含路径分隔符按路径用，否则 PATH 解析。空 = "multica"。
	Binary string
	// Profile 配置 profile 名（根命令全局选项，插在子命令前）。
	Profile string
	// ServerURL 服务器地址覆盖（--server-url）。
	ServerURL string
	// WorkspaceID 目标 workspace（--workspace-id）。
	WorkspaceID string
	// Timeout 单次调用超时。
	Timeout time.Duration
	// Runner 注入点（测试用 fake）；nil = 真实 exec。
	Runner Runner
}

// Runner 抽象一次子进程执行（stdout/stderr/退出错误），便于测试注入。
type Runner interface {
	Run(ctx context.Context, exe string, args []string, env []string) (stdout string, stderr string, err error)
}

// runnerFunc 函数适配器。
type runnerFunc func(ctx context.Context, exe string, args []string, env []string) (string, string, error)

func (f runnerFunc) Run(ctx context.Context, exe string, args []string, env []string) (string, string, error) {
	return f(ctx, exe, args, env)
}

// execRunner 默认执行器：隐藏窗口（Windows）、继承环境、截获 stdout/stderr。
type execRunner struct{}

func (execRunner) Run(ctx context.Context, exe string, args []string, env []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	hideWindow(cmd)
	if len(env) > 0 {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// ResolveBinary 解析可执行文件：显式路径直接用（存在即成功），否则 LookPath。
func (c *CLI) ResolveBinary() (string, error) {
	name := c.Binary
	if name == "" {
		name = "multica"
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, '\\') || strings.ContainsRune(name, ':') {
		if _, err := os.Stat(name); err != nil {
			return "", &Error{Code: CodeCLIMissing, Message: fmt.Sprintf("multica CLI 不存在: %s", name), Hint: "检查 settings.multica.binary 路径，或安装 multica CLI"}
		}
		return name, nil
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", &Error{
			Code:    CodeCLIMissing,
			Message: "找不到 multica CLI（PATH 中无此命令）",
			Hint:    "安装 multica CLI 后重试；或在面板「Multica → 连接」中指定 CLI 路径",
		}
	}
	return path, nil
}

// run 执行一条 multica 子命令。prefix 全局选项（--profile 等）+ args 子命令与参数。
// 返回 stdout；失败总是 *Error。
func (c *CLI) run(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	stdout, _, err := c.runBoth(ctx, timeout, args...)
	return stdout, err
}

// runBoth 同 run，但额外返回 stderr（auth status 等命令把结果写 stderr，解析方需要）。
func (c *CLI) runBoth(ctx context.Context, timeout time.Duration, args ...string) (string, string, error) {
	if c.Timeout > 0 {
		timeout = c.Timeout
	}
	if timeout <= 0 {
		timeout = WriteTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	exe, err := c.ResolveBinary()
	if err != nil {
		return "", "", err
	}

	prefix := []string{}
	if c.Profile != "" {
		prefix = append(prefix, "--profile", c.Profile)
	}
	if c.ServerURL != "" {
		prefix = append(prefix, "--server-url", c.ServerURL)
	}
	if c.WorkspaceID != "" {
		prefix = append(prefix, "--workspace-id", c.WorkspaceID)
	}
	full := append(prefix, args...)

	env := os.Environ()
	if c.Runner != nil {
		stdout, stderr, runErr := c.Runner.Run(runCtx, exe, full, env)
		if runErr != nil {
			return stdout, stderr, classify(runErr, stderr, args)
		}
		return stdout, stderr, nil
	}
	stdout, stderr, runErr := execRunner{}.Run(runCtx, exe, full, env)
	if runErr != nil {
		return stdout, stderr, classify(runErr, stderr, args)
	}
	return stdout, stderr, nil
}

// runRaw 执行并返回 stdout 的原始 JSON（容忍形状由调用方解析）。
func (c *CLI) runRaw(ctx context.Context, timeout time.Duration, args ...string) (json.RawMessage, error) {
	stdout, err := c.run(ctx, timeout, args...)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage("[]"), nil
	}
	return json.RawMessage(trimmed), nil
}

// runJSON 执行并解析 stdout 为 JSON。
func (c *CLI) runJSON(ctx context.Context, timeout time.Duration, out any, args ...string) error {
	raw, err := c.runRaw(ctx, timeout, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{
			Code:    CodeUnknown,
			Message: "multica CLI 返回的不是合法 JSON",
			Detail:  truncate(string(raw), 300),
		}
	}
	return nil
}

// tempConfigFile 把 MCP 配置（或技能内容）写成临时文件（凭据只在内存与该文件存在，
// 调用方 defer cleanup 后立即删除）。0600 权限 + Windows 隐藏属性。
func (c *CLI) tempConfigFile(content string, pattern string) (path string, cleanup func(), err error) {
	dir := os.TempDir()
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", func() {}, &Error{Code: CodeUnknown, Message: "创建临时文件失败", Detail: err.Error()}
	}
	name := f.Name()
	_ = f.Chmod(0o600)
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", func() {}, &Error{Code: CodeUnknown, Message: "写入临时文件失败", Detail: err.Error()}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", func() {}, &Error{Code: CodeUnknown, Message: "写入临时文件失败", Detail: err.Error()}
	}
	hideFile(name)
	return name, func() { _ = os.Remove(name) }, nil
}

// TempFile 导出的临时内容文件（daemon 层传 SKILL.md 内容等给 --content-file）。
func (c *CLI) TempFile(content string, pattern string) (string, func(), error) {
	return c.tempConfigFile(content, pattern)
}

// validateConfigJSON MCP 条目必须是 JSON 对象（mcpServers 下同一形状）。
func validateConfigJSON(configJSON string) error {
	trimmed := strings.TrimSpace(configJSON)
	if trimmed == "" {
		return &Error{Code: CodeInvalid, Message: "MCP 配置不能为空"}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil || obj == nil {
		return &Error{Code: CodeInvalid, Message: "MCP 配置必须是 JSON 对象（与 mcpServers 下条目同形状）"}
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
