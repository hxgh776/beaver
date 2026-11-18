package multica

import (
	"context"
	"regexp"
	"strings"
)

// AuthStatus multica CLI 登录态（beaver 不存 token，登录态完全来自 CLI profile）。
type AuthStatus struct {
	LoggedIn bool   `json:"loggedIn"`
	User     string `json:"user,omitempty"`
	Server   string `json:"server,omitempty"`
}

var (
	reUserLine   = regexp.MustCompile(`(?m)^User:\s+(.+)$`)
	reServerLine = regexp.MustCompile(`(?m)^Server:\s+(.+)$`)
)

// AuthStatus 探测 CLI 登录态。退出非 0 或 User 行缺失（stdout/stderr 都看）→ 未登录。
// 权限/网络类失败原样上抛（报「未登录」会误导诊断）；CLI 缺失同样上抛。
func (c *CLI) AuthStatus(ctx context.Context) (*AuthStatus, error) {
	stdout, stderr, err := c.runBoth(ctx, ProbeTimeout, "auth", "status")
	if err != nil {
		// classify 已按文本分类；auth status 失败最常见的成因是未登录，
		// 归一为 NotLoggedIn，让面板给出「运行 multica login」这一个明确动作。
		if me, ok := err.(*Error); ok {
			switch me.Code {
			case CodeCLIMissing, CodePermission, CodeUnreachable:
				return nil, me
			}
			return &AuthStatus{LoggedIn: false}, nil
		}
		return nil, err
	}
	// 同一 CLI 家族里 auth status 的落点不一致（有的版本写 stdout，有的写 stderr），两边都解析
	user := ""
	server := ""
	for _, text := range []string{stdout, stderr} {
		if user == "" {
			if m := reUserLine.FindStringSubmatch(text); m != nil {
				user = strings.TrimSpace(m[1])
			}
		}
		if server == "" {
			if m := reServerLine.FindStringSubmatch(text); m != nil {
				server = strings.TrimSpace(m[1])
			}
		}
	}
	if user == "" {
		return &AuthStatus{LoggedIn: false, Server: server}, nil
	}
	return &AuthStatus{LoggedIn: true, User: user, Server: server}, nil
}

// Workspace 用户所属的 Multica workspace。
type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// ListWorkspaces 枚举登录账号所属 workspace（workspace 切换的候选来源）。
func (c *CLI) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	// 切换列表必须不带 --workspace-id（要看的是全部候选）；浅拷贝避免动到并发共享的 c
	cp := *c
	cp.WorkspaceID = ""
	var out []Workspace
	if err := cp.runJSON(ctx, ListTimeout, &out, "workspace", "list", "--output", "json"); err != nil {
		return nil, err
	}
	return out, nil
}
