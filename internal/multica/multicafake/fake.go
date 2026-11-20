// Package multicafake 提供 multica CLI 的 fake 实现，供测试二进制自举重入
//（同 internal/gateway 的 TestHelperFakeStdio 模式：BEAVER_FAKE_MULTICA=1 时
// 测试进程在 TestMain 里转手给 Main，按真实 CLI 的输出形状给出 canned 响应）。
//
// 行为：
//   - 每次调用把 argv 追加到 BEAVER_FAKE_MULTICA_LOG（\x1f 分隔），供断言参数构造；
//   - 读到 --server-config-file 时把文件内容以 config=<content> 记进日志，供断言
//     凭据传递通道与临时文件生命周期；
//   - BEAVER_FAKE_MULTICA_FAIL 非空 → 该文本写 stderr 并退出 2（失败语义注入）；
//   - BEAVER_FAKE_MULTICA_NOAUTH=1 → auth status 未登录。
package multicafake

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// 环境变量与日志分隔符导出，测试侧共用。
const (
	Env        = "BEAVER_FAKE_MULTICA"
	EnvLog     = "BEAVER_FAKE_MULTICA_LOG"
	EnvFail    = "BEAVER_FAKE_MULTICA_FAIL"
	EnvNoAuth  = "BEAVER_FAKE_MULTICA_NOAUTH"
	EnvState   = "BEAVER_FAKE_MULTICA_STATE"
	EnvFiles   = "BEAVER_FAKE_MULTICA_FILES" // 覆盖 skill files list 的 JSON 输出
	ArgvSuffix = "\x1f"                      // 日志行内参数分隔
)

// stateFile 有状态库（BEAVER_FAKE_MULTICA_STATE 指向 JSON 文件；每次调用读改写，
// 跨子进程持久）——workspace mcp add/update/remove 与 skill create/delete 真实生效，
// list 反映结果，供收敛删除、投影移除等断言使用。无 state 文件时 list 走
// canned 默认（老测试不受影响）。
type stateFile struct {
	Servers []fakeServer `json:"servers"`
	Skills  []fakeSkill  `json:"skills"`
}

type fakeServer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Transport string `json:"transport"`
}

type fakeSkill struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func loadState() *stateFile {
	path := os.Getenv(EnvState)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return &stateFile{}
	}
	st := &stateFile{}
	if json.Unmarshal(data, st) != nil {
		return &stateFile{}
	}
	return st
}

func saveState(st *stateFile) {
	path := os.Getenv(EnvState)
	if path == "" {
		return
	}
	data, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(path, data, 0o600)
}

// Main 处理一次 fake CLI 调用，返回进程退出码。
func Main(argv []string) int {
	var args []string
	for _, a := range argv {
		if strings.HasPrefix(a, "-test.") {
			continue // go test 旗标防御性跳过
		}
		args = append(args, a)
	}
	logLine(strings.Join(args, ArgvSuffix))

	if fail := os.Getenv(EnvFail); fail != "" {
		fmt.Fprintln(os.Stderr, fail)
		return 2
	}

	// 剥离全局选项（--profile/--server-url/--workspace-id）
	i := 0
	for i+1 < len(args) {
		switch args[i] {
		case "--profile", "--server-url", "--workspace-id":
			i += 2
			continue
		}
		break
	}
	rest := args[i:]

	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "unknown command")
		return 2
	}

	var out string
	switch {
	case rest[0] == "auth" && len(rest) >= 2 && rest[1] == "status":
		if os.Getenv(EnvNoAuth) == "1" {
			fmt.Fprintln(os.Stderr, "error: not logged in. Run 'multica login' to authenticate.")
			return 1
		}
		out = "Server:  https://api.multica.ai\nUser:    tester (tester@example.com)\n"
	case rest[0] == "workspace" && len(rest) >= 2 && rest[1] == "list":
		out = `[{"id":"ws-1","name":"金星","slug":"venus-4gw6"},{"id":"ws-2","name":"火星","slug":"mars"}]`
	case rest[0] == "workspace" && len(rest) >= 3 && rest[1] == "mcp" && rest[2] == "list":
		if st := loadState(); st != nil {
			data, _ := json.Marshal(st.Servers)
			out = string(data)
		} else {
			out = `[{"id":"srv-1","name":"linear","transport":"http"},{"id":"srv-2","name":"fs","transport":"stdio"}]`
		}
	case rest[0] == "workspace" && len(rest) >= 3 && rest[1] == "mcp" && (rest[2] == "add" || rest[2] == "update"):
		logConfig(rest)
		name := ""
		if rest[2] == "add" && len(rest) > 3 {
			name = rest[3]
		}
		if st := loadState(); st != nil {
			if rest[2] == "add" {
				id := fmt.Sprintf("srv-%d", len(st.Servers)+1)
				transport := "http"
				if strings.Contains(loggedConfig(rest, "--server-config-file"), `"command"`) {
					transport = "stdio"
				}
				st.Servers = append(st.Servers, fakeServer{ID: id, Name: name, Transport: transport})
				saveState(st)
				out = fmt.Sprintf(`{"id":%q,"name":%q}`, id, name)
			} else {
				out = `{"ok":true}` // update：fake 不存配置本体，只回 ok
			}
			break
		}
		out = fmt.Sprintf(`{"id":"srv-x","name":%q}`, name)
	case rest[0] == "workspace" && len(rest) >= 3 && rest[1] == "mcp" && rest[2] == "remove":
		if st := loadState(); st != nil && len(rest) > 3 {
			kept := st.Servers[:0]
			for _, s := range st.Servers {
				if s.ID != rest[3] {
					kept = append(kept, s)
				}
			}
			st.Servers = kept
			saveState(st)
		}
		out = `{"ok":true}`
	case rest[0] == "agent" && len(rest) >= 2 && rest[1] == "list":
		out = `[{"id":"agent-1","name":"WorkBuddy","status":"idle","model":"deepseek-v4.1-flash"},{"id":"agent-2","name":"Qoder","status":"busy","model":"dfmodel"}]`
	case rest[0] == "agent" && len(rest) >= 4 && rest[1] == "mcp" && rest[2] == "list":
		if rest[3] == "agent-1" {
			out = `[{"server_id":"srv-1","name":"linear","enabled":true}]`
		} else {
			out = `[]`
		}
	case rest[0] == "agent" && len(rest) >= 4 && rest[1] == "mcp":
		out = `{"ok":true}`
	case rest[0] == "skill" && len(rest) >= 2 && rest[1] == "list":
		if st := loadState(); st != nil {
			data, _ := json.Marshal(st.Skills)
			out = string(data)
		} else {
			out = `[{"config":{"origin":{"type":"runtime_local"}},"created_at":"2026-11-14T04:42:50Z","created_by":"u-1","description":"Automates browser interactions.","id":"sk-1","labels":["web"],"name":"browser-use","updated_at":"2026-11-14T04:42:50Z","workspace_id":"ws-1"}]`
		}
	case rest[0] == "skill" && len(rest) >= 2 && rest[1] == "search":
		out = `[{"description":"Browser automation CLI.","github_stars":null,"install_count":3,"name":"browser","repo":null,"source":"clawhub.ai","url":"https://clawhub.ai/x/browser"}]`
	case rest[0] == "skill" && len(rest) >= 2 && rest[1] == "import":
		out = `{"id":"sk-new","name":"imported-skill"}`
	case rest[0] == "skill" && len(rest) >= 2 && rest[1] == "create":
		logConfig(rest)
		name := ""
		for j, a := range rest {
			if a == "--name" && j+1 < len(rest) {
				name = rest[j+1]
			}
		}
		if st := loadState(); st != nil {
			id := fmt.Sprintf("sk-%d", len(st.Skills)+1)
			st.Skills = append(st.Skills, fakeSkill{ID: id, Name: name})
			saveState(st)
			out = fmt.Sprintf(`{"id":%q,"name":%q}`, id, name)
		} else {
			out = `{"id":"sk-c","name":"created"}`
		}
	case rest[0] == "skill" && len(rest) >= 3 && rest[1] == "update":
		logConfig(rest)
		out = `{"id":"sk-c","name":"updated"}`
	case rest[0] == "skill" && len(rest) >= 3 && rest[1] == "delete":
		if st := loadState(); st != nil {
			kept := st.Skills[:0]
			for _, s := range st.Skills {
				if s.ID != rest[2] {
					kept = append(kept, s)
				}
			}
			st.Skills = kept
			saveState(st)
		}
		out = `{"ok":true}`
	case rest[0] == "skill" && len(rest) >= 4 && rest[1] == "files" && rest[2] == "list":
		if files := os.Getenv(EnvFiles); files != "" {
			out = files
		} else {
			out = `[{"id":"f-1","path":"SKILL.md","size":120},{"id":"f-2","path":"scripts/run.sh","size":300}]`
		}
	case rest[0] == "skill" && len(rest) >= 4 && rest[1] == "files" && (rest[2] == "upsert" || rest[2] == "delete"):
		logConfig(rest)
		out = `{"ok":true}`
	default:
		fmt.Fprintln(os.Stderr, "unknown command: "+strings.Join(rest, " "))
		return 2
	}
	if out != "" {
		fmt.Fprintln(os.Stdout, out)
	}
	return 0
}

// logConfig fake add/update 时读取 --server-config-file 内容记进日志（用后即焚前的唯一快照）。
// --content-file（skill create/update/files upsert 的正文通道）以 content= 前缀记录。
func logConfig(args []string) {
	for j, a := range args {
		if a == "--content-file" && j+1 < len(args) {
			if data, err := os.ReadFile(args[j+1]); err == nil {
				logLine("content=" + string(data))
			} else {
				logLine("content-read-error=" + err.Error())
			}
			return
		}
	}
	if cfg := loggedConfig(args, "--server-config-file"); cfg != "" {
		logLine("config=" + cfg)
	} else {
		logLine("config=<missing>")
	}
}

// loggedConfig 读取指定旗标指向的文件内容（缺失/失败返回空串）。
func loggedConfig(args []string, flag string) string {
	for j, a := range args {
		if a == flag && j+1 < len(args) {
			data, err := os.ReadFile(args[j+1])
			if err != nil {
				return ""
			}
			return string(data)
		}
	}
	return ""
}

func logLine(line string) {
	if path := os.Getenv(EnvLog); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(line + "\n")
			_ = f.Close()
		}
	}
}
