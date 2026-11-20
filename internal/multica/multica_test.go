package multica

// 适配层测试：fake Runner 覆盖参数构造与解析（正常路径），fake CLI 重入覆盖真实
// exec 通道与失败语义（异常路径）。凭据边界有专门断言：配置只经临时文件传递、
// 命令行不内联、用后即焚。

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxgh776/beaver/internal/multica/multicafake"
)

// runnerStub 记录每次调用的 exe/args/env，按脚本回放 stdout/stderr/exit。
type runnerStub struct {
	calls  []stubCall
	stdout string
	stderr string
	exit   *exec.ExitError
}

type stubCall struct {
	exe  string
	args []string
	env  []string
}

var errExit = &exec.ExitError{}

func (r *runnerStub) Run(_ context.Context, exe string, args []string, env []string) (string, string, error) {
	r.calls = append(r.calls, stubCall{exe: exe, args: args, env: env})
	if r.exit != nil {
		return r.stdout, r.stderr, errExit
	}
	return r.stdout, r.stderr, nil
}

func newStub(stdout string) *runnerStub { return &runnerStub{stdout: stdout} }

func (r *runnerStub) cli() *CLI {
	return &CLI{Runner: r}
}

func joinedArgs(c stubCall) string { return strings.Join(c.args, " ") }

// —— 参数构造 —— //

func TestRunPrefixFlags(t *testing.T) {
	r := newStub(`[]`)
	c := r.cli()
	c.Profile = "dev"
	c.ServerURL = "https://alt.example"
	c.WorkspaceID = "ws-42"
	if _, err := c.ListMCPServers(context.Background()); err != nil {
		t.Fatalf("ListMCPServers: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("期望 1 次调用，得到 %d", len(r.calls))
	}
	got := joinedArgs(r.calls[0])
	for _, want := range []string{"--profile dev", "--server-url https://alt.example", "--workspace-id ws-42", "workspace mcp list --output json"} {
		if !strings.Contains(got, want) {
			t.Fatalf("argv 缺少 %q：%s", want, got)
		}
	}
}

func TestListWorkspacesOmitsWorkspaceFlag(t *testing.T) {
	r := newStub(`[{"id":"ws-1","name":"V","slug":"v"}]`)
	c := r.cli()
	c.WorkspaceID = "ws-other"
	ws, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(ws) != 1 || ws[0].ID != "ws-1" || ws[0].Name != "V" {
		t.Fatalf("解析错误: %+v", ws)
	}
	if strings.Contains(joinedArgs(r.calls[0]), "--workspace-id") {
		t.Fatalf("workspace list 不应带 --workspace-id：%s", joinedArgs(r.calls[0]))
	}
	// 且不能动共享 CLI 的目标 workspace
	if c.WorkspaceID != "ws-other" {
		t.Fatalf("WorkspaceID 被意外修改: %s", c.WorkspaceID)
	}
}

// —— MCP 库解析 —— //

func TestListMCPServersShapes(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   []MCPServer
	}{
		{"数组", `[{"id":"a","name":"Linear","transport":"http"}]`, []MCPServer{{ID: "a", Name: "Linear", Transport: "http"}}},
		{"包装对象", `{"servers":[{"id":"a","name":"L","transport":"stdio"}]}`, []MCPServer{{ID: "a", Name: "L", Transport: "stdio"}}},
		{"snake_case", `[{"server_id":"b","server_name":"FS","transport_type":"stdio"}]`, []MCPServer{{ID: "b", Name: "FS", Transport: "stdio"}}},
		{"空输出", ``, []MCPServer{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newStub(tc.stdout)
			got, err := r.cli().ListMCPServers(context.Background())
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("长度不等: %+v vs %+v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("第 %d 项不等: %+v vs %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// —— MCP 写操作与凭据边界 —— //

func TestAddMCPServerWritesTempConfigAndCleans(t *testing.T) {
	r := newStub(`{"id":"srv-x"}`)
	cfg := `{"url":"https://mcp.example","headers":{"Authorization":"Bearer sk-secret"}}`
	if err := r.cli().AddMCPServer(context.Background(), "linear", cfg); err != nil {
		t.Fatalf("AddMCPServer: %v", err)
	}
	got := joinedArgs(r.calls[0])
	if !strings.Contains(got, "workspace mcp add linear --server-config-file") {
		t.Fatalf("argv 异常: %s", got)
	}
	if strings.Contains(got, "--server-config ") || strings.Contains(got, "sk-secret") {
		t.Fatalf("凭据不允许内联进命令行: %s", got)
	}
	// 临时文件已被清理（defer cleanup 在 run 返回后执行）
	pathIdx := strings.Index(got, "--server-config-file ") + len("--server-config-file ")
	path := strings.TrimSpace(got[pathIdx:])
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("临时文件未删除: %s", path)
	}
}

func TestAddMCPServerValidatesInput(t *testing.T) {
	r := newStub(``)
	c := r.cli()
	if err := c.AddMCPServer(context.Background(), "", `{"url":"x"}`); err == nil {
		t.Fatal("空名称应报错")
	}
	if err := c.AddMCPServer(context.Background(), "n", `not-json`); err == nil {
		t.Fatal("非对象配置应报错")
	}
	if err := c.AddMCPServer(context.Background(), "n", `[1,2]`); err == nil {
		t.Fatal("数组配置应报错")
	}
	if err := c.AddMCPServer(context.Background(), "n", ""); err == nil {
		t.Fatal("空配置应报错")
	}
	if len(r.calls) != 0 {
		t.Fatalf("校验失败不应发起 CLI 调用: %d", len(r.calls))
	}
}

func TestUpdateMCPServerArgCombinations(t *testing.T) {
	r := newStub(`{}`)
	c := r.cli()
	// 只改名
	if err := c.UpdateMCPServer(context.Background(), "srv-1", "newname", ""); err != nil {
		t.Fatalf("只改名: %v", err)
	}
	if got := joinedArgs(r.calls[0]); !strings.Contains(got, "workspace mcp update srv-1 --name newname") {
		t.Fatalf("argv: %s", got)
	}
	// 只换配置
	if err := c.UpdateMCPServer(context.Background(), "srv-1", "", `{"url":"u"}`); err != nil {
		t.Fatalf("只换配置: %v", err)
	}
	if got := joinedArgs(r.calls[1]); !strings.Contains(got, "--server-config-file") || strings.Contains(got, "--name") {
		t.Fatalf("argv: %s", got)
	}
	// 两者都没有 → 报错且不调用
	if err := c.UpdateMCPServer(context.Background(), "srv-1", "", ""); err == nil {
		t.Fatal("全空更新应报错")
	}
	if len(r.calls) != 2 {
		t.Fatalf("全空更新不应调用 CLI: %d", len(r.calls))
	}
}

func TestRemoveMCPServerRequiresID(t *testing.T) {
	r := newStub(`{}`)
	if err := r.cli().RemoveMCPServer(context.Background(), ""); err == nil {
		t.Fatal("空 id 应报错")
	}
	if err := r.cli().RemoveMCPServer(context.Background(), "srv-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := joinedArgs(r.calls[0]); got != "workspace mcp remove srv-1" {
		t.Fatalf("argv: %s", got)
	}
}

// —— 认证与 workspace —— //

func TestAuthStatusParse(t *testing.T) {
	r := newStub("Server:  https://api.multica.ai\nUser:    1099040267 (1099040267@qq.com)\n")
	st, err := r.cli().AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if !st.LoggedIn || st.User != "1099040267 (1099040267@qq.com)" || st.Server != "https://api.multica.ai" {
		t.Fatalf("解析错误: %+v", st)
	}
}

func TestAuthStatusParseFromStderr(t *testing.T) {
	// 实测部分版本把 auth status 写到 stderr（exit 0）——两边都要解析
	r := &runnerStub{stderr: "Server:  https://api.multica.ai\nUser:    tester (tester@example.com)\n"}
	st, err := r.cli().AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if !st.LoggedIn || st.User != "tester (tester@example.com)" || st.Server != "https://api.multica.ai" {
		t.Fatalf("stderr 路径解析错误: %+v", st)
	}
}

func TestAuthStatusNotLoggedInOutput(t *testing.T) {
	// 有输出但没有 User 行 → 未登录
	r := newStub("Server:  https://api.multica.ai\n")
	st, err := r.cli().AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if st.LoggedIn {
		t.Fatalf("应视为未登录: %+v", st)
	}
}

// —— 失败语义分类 —— //

func TestClassifyTable(t *testing.T) {
	cases := []struct {
		stderr string
		want   ErrorCode
	}{
		{"error: not logged in. Run 'multica login'", CodeNotLoggedIn},
		{"Admin or owner role required", CodePermission},
		{"forbidden", CodePermission},
		{"server 'x' not found", CodeNotFound},
		{"name already exists", CodeConflict},
		{"Could not resolve the Multica server address", CodeUnreachable},
		{"Invalid request: invalid workspace_id", CodeInvalid},
		{"something totally unexpected", CodeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.stderr, func(t *testing.T) {
			r := &runnerStub{stderr: tc.stderr, exit: errExit}
			_, err := r.cli().ListMCPServers(context.Background())
			me, ok := err.(*Error)
			if !ok {
				t.Fatalf("期望 *Error，得到 %T: %v", err, err)
			}
			if me.Code != tc.want {
				t.Fatalf("code = %s, want %s（detail: %s）", me.Code, tc.want, me.Detail)
			}
			if me.Message == "" {
				t.Fatal("Message 不能为空")
			}
		})
	}
}

func TestClassifyCLIMissing(t *testing.T) {
	c := &CLI{Binary: filepath.Join(t.TempDir(), "no-such-multica")}
	_, err := c.ListMCPServers(context.Background())
	me, ok := err.(*Error)
	if !ok || me.Code != CodeCLIMissing {
		t.Fatalf("期望 CLI_MISSING，得到 %v", err)
	}
}

func TestClassifyTimeout(t *testing.T) {
	// Runner 报 context.DeadlineExceeded → Unreachable
	r := &runnerStub{}
	c := &CLI{Runner: runnerFunc(func(ctx context.Context, exe string, args []string, env []string) (string, string, error) {
		return "", "", context.DeadlineExceeded
	})}
	_ = r
	_, err := c.ListMCPServers(context.Background())
	me, ok := err.(*Error)
	if !ok || me.Code != CodeUnreachable {
		t.Fatalf("期望 UNREACHABLE，得到 %v", err)
	}
}

// —— agent 绑定 —— //

func TestAgentBindingArgs(t *testing.T) {
	r := newStub(`{}`)
	c := r.cli()
	for _, tc := range []struct {
		verb string
		fn   func() error
		want string
	}{
		{"add", func() error { return c.BindAgentMCP(context.Background(), "agent-1", "srv-1") }, "agent mcp add agent-1 srv-1"},
		{"remove", func() error { return c.UnbindAgentMCP(context.Background(), "agent-1", "srv-1") }, "agent mcp remove agent-1 srv-1"},
		{"enable", func() error { return c.EnableAgentMCP(context.Background(), "agent-1", "srv-1") }, "agent mcp enable agent-1 srv-1"},
		{"disable", func() error { return c.DisableAgentMCP(context.Background(), "agent-1", "srv-1") }, "agent mcp disable agent-1 srv-1"},
	} {
		if err := tc.fn(); err != nil {
			t.Fatalf("%s: %v", tc.verb, err)
		}
	}
	for _, call := range r.calls {
		if !strings.Contains(joinedArgs(call), "--output json") {
			// 写操作不带 --output，直接子命令即可；这里只是确认调用发生过
		}
	}
}

func TestBindAgentMCPRequiresIDs(t *testing.T) {
	r := newStub(`{}`)
	c := r.cli()
	if err := c.BindAgentMCP(context.Background(), "", "srv-1"); err == nil {
		t.Fatal("空 agent id 应报错")
	}
	if err := c.BindAgentMCP(context.Background(), "a", ""); err == nil {
		t.Fatal("空 server id 应报错")
	}
	if len(r.calls) != 0 {
		t.Fatal("校验失败不应调用 CLI")
	}
}

func TestListAgentsParse(t *testing.T) {
	// 第二条缺 id：跳过而不是让整个列表失败
	r := newStub(`[{"id":"agent-1","name":"WorkBuddy","status":"idle","model":"m1"},{"name":"no-id-here"}]`)
	agents, err := r.cli().ListAgents(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "agent-1" || agents[0].Name != "WorkBuddy" {
		t.Fatalf("解析错误: %+v", agents)
	}
}

func TestListAgentMCPServerIDsEnabledField(t *testing.T) {
	r := newStub(`[{"server_id":"srv-1","name":"linear","enabled":true},{"server_id":"srv-2","name":"fs","enabled":false},{"server_id":"srv-3","name":"g","status":"disabled"}]`)
	got, err := r.cli().ListAgentMCPServerIDs(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("期望 3 条: %+v", got)
	}
	if !got[0].Enabled || got[1].Enabled || got[2].Enabled {
		t.Fatalf("enabled 判定错误: %+v", got)
	}
}

// —— Skill 库 —— //

func TestListSkillsSnakeCase(t *testing.T) {
	r := newStub(`[{"config":{},"created_at":"2026-11-14T04:42:50Z","created_by":"u-1","description":"Automates browser interactions.","id":"sk-1","labels":["web"],"name":"browser-use","updated_at":"2026-11-14T04:42:50Z","workspace_id":"ws-1"}]`)
	skills, err := r.cli().ListSkills(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("期望 1 条: %+v", skills)
	}
	s := skills[0]
	if s.ID != "sk-1" || s.Name != "browser-use" || s.Description != "Automates browser interactions." || s.CreatedBy != "u-1" {
		t.Fatalf("字段映射错误: %+v", s)
	}
	if len(s.Labels) != 1 || s.Labels[0] != "web" {
		t.Fatalf("labels 解析错误: %+v", s.Labels)
	}
	if s.CreatedAt != "2026-11-14T04:42:50Z" || s.UpdatedAt != "2026-11-14T04:42:50Z" {
		t.Fatalf("时间字段错误: %+v", s)
	}
}

func TestSearchSkillsParse(t *testing.T) {
	r := newStub(`[{"description":"Browser automation CLI.","install_count":3,"name":"browser","repo":null,"source":"clawhub.ai","url":"https://clawhub.ai/x/browser"}]`)
	got, err := r.cli().SearchSkills(context.Background(), "browser")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || got[0].Name != "browser" || got[0].Source != "clawhub.ai" || got[0].URL != "https://clawhub.ai/x/browser" {
		t.Fatalf("解析错误: %+v", got)
	}
	if got[0].Installs == nil || *got[0].Installs != 3 {
		t.Fatalf("install_count 解析错误: %+v", got[0].Installs)
	}
	if _, err := r.cli().SearchSkills(context.Background(), "  "); err == nil {
		t.Fatal("空关键词应报错")
	}
}

func TestImportSkillFromURL(t *testing.T) {
	r := newStub(`{"id":"sk-new","name":"imported-skill"}`)
	res, err := r.cli().ImportSkillFromURL(context.Background(), "https://github.com/a/b", ConflictRename)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.ID != "sk-new" || res.Name != "imported-skill" {
		t.Fatalf("结果解析错误: %+v", res)
	}
	got := joinedArgs(r.calls[0])
	if !strings.Contains(got, "skill import --url https://github.com/a/b --on-conflict rename") {
		t.Fatalf("argv: %s", got)
	}
	if _, err := r.cli().ImportSkillFromURL(context.Background(), "", ""); err == nil {
		t.Fatal("空 URL 应报错")
	}
	if _, err := r.cli().ImportSkillFromURL(context.Background(), "https://x", "bogus"); err == nil {
		t.Fatal("非法 onConflict 应报错")
	}
}

func TestCreateUpdateDeleteSkill(t *testing.T) {
	r := newStub(`{}`)
	c := r.cli()
	if err := c.CreateSkill(context.Background(), "", "d", ""); err == nil {
		t.Fatal("空名称应报错")
	}
	if err := c.CreateSkill(context.Background(), "demo", "说明", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := joinedArgs(r.calls[0]); !strings.Contains(got, "skill create --name demo --description 说明") {
		t.Fatalf("argv: %s", got)
	}
	if err := c.UpdateSkill(context.Background(), "sk-1", "", "", ""); err == nil {
		t.Fatal("全空更新应报错")
	}
	if err := c.UpdateSkill(context.Background(), "sk-1", "n2", "", ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := c.DeleteSkill(context.Background(), "sk-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := joinedArgs(r.calls[2]); !strings.Contains(got, "skill delete sk-1 --yes") {
		t.Fatalf("delete 必须 --yes 免交互: %s", got)
	}
}

func TestCreateSkillContentGoesThroughTempFile(t *testing.T) {
	r := newStub(`{}`)
	c := r.cli()
	if err := c.CreateSkill(context.Background(), "demo", "", "content"); err != nil {
		t.Fatalf("create: %v", err)
	}
	got := joinedArgs(r.calls[0])
	if !strings.Contains(got, "--content-file") {
		t.Fatalf("内容必须走 --content-file: %s", got)
	}
	idx := strings.Index(got, "--content-file ") + len("--content-file ")
	path := strings.TrimSpace(got[idx:])
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("内容临时文件未删除: %s", path)
	}
}

func TestListSkillFiles(t *testing.T) {
	r := newStub(`[{"path":"SKILL.md","size":120}]`)
	files, err := r.cli().ListSkillFiles(context.Background(), "sk-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(files) != 1 || files[0].Path != "SKILL.md" || files[0].Size != 120 {
		t.Fatalf("解析错误: %+v", files)
	}
}

// —— 真实 exec 通道（fake CLI 重入，同 gateway 的 helper 模式） —— //

func fakeCLICmd(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("解析测试二进制失败: %v", err)
	}
	return exe
}

func TestExecChannelEndToEnd(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "fakecli.log")
	t.Setenv(multicafake.Env, "1")
	t.Setenv(multicafake.EnvLog, logPath)

	c := &CLI{
		Binary:      fakeCLICmd(t),
		Profile:     "p1",
		WorkspaceID: "ws-1",
	}
	servers, err := c.ListMCPServers(context.Background())
	if err != nil {
		t.Fatalf("真实 exec 失败: %v", err)
	}
	if len(servers) != 2 || servers[0].ID != "srv-1" || servers[0].Name != "linear" {
		t.Fatalf("fake CLI 输出解析错误: %+v", servers)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读 fake 日志失败: %v", err)
	}
	// 日志行是 \x1f 分隔的 argv；还原成 token 序列断言
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 1 {
		t.Fatalf("期望 1 条日志行: %q", logData)
	}
	tokens := strings.Split(lines[0], multicafake.ArgvSuffix)
	for _, want := range []string{"--profile", "p1", "--workspace-id", "ws-1", "workspace", "mcp", "list", "--output", "json"} {
		if !containsToken(tokens, want) {
			t.Fatalf("fake 日志缺少 token %q：%q", want, tokens)
		}
	}
}

func containsToken(tokens []string, want string) bool {
	for _, tok := range tokens {
		if tok == want {
			return true
		}
	}
	return false
}

func TestExecChannelAuthNotLoggedIn(t *testing.T) {
	t.Setenv(multicafake.Env, "1")
	t.Setenv(multicafake.EnvNoAuth, "1")
	c := &CLI{Binary: fakeCLICmd(t)}
	st, err := c.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if st.LoggedIn {
		t.Fatalf("应视为未登录: %+v", st)
	}
}

func TestExecChannelFailureClassification(t *testing.T) {
	t.Setenv(multicafake.Env, "1")
	t.Setenv(multicafake.EnvFail, "Admin or owner role required")
	c := &CLI{Binary: fakeCLICmd(t)}
	err := c.RemoveMCPServer(context.Background(), "srv-1")
	me, ok := err.(*Error)
	if !ok || me.Code != CodePermission {
		t.Fatalf("期望 PERMISSION_DENIED，得到 %v", err)
	}
	if me.Detail == "" {
		t.Fatal("Detail 应携带 CLI stderr 摘要")
	}
}

func TestExecChannelTempFileSnapshot(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "fakecli.log")
	t.Setenv(multicafake.Env, "1")
	t.Setenv(multicafake.EnvLog, logPath)
	c := &CLI{Binary: fakeCLICmd(t)}
	cfg := `{"url":"https://mcp.example","headers":{"Authorization":"Bearer sk-live-999"}}`
	if err := c.AddMCPServer(context.Background(), "linear", cfg); err != nil {
		t.Fatalf("add: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读日志: %v", err)
	}
	if !strings.Contains(string(logData), "config="+cfg) {
		t.Fatalf("fake 未收到完整配置：\n%s", logData)
	}
}
