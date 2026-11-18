package tui

// Multica 连接与远端库状态：「Agent 分发」页的「Multica 连接」入口。
// multica 是 CLI 外置型 agent（MCP/Skill 落在 Multica workspace 库，无本地配置文件），
// 这里只做连接诊断与 workspace 切换；平台内部把条目分给哪些智能体不归 beaver 管。

import (
	"fmt"
	"strings"

	"github.com/rivo/tview"
)

// MulticaSectionError 远端分区错误（code/message/hint，与 daemon 响应同形）。
type MulticaSectionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// MulticaWorkspace 登录账号所属 workspace。
type MulticaWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// MulticaMCPServer workspace MCP 库条目（配置本体 write-only，只回元数据）。
type MulticaMCPServer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Transport string `json:"transport"`
}

// MulticaSkill workspace Skill 库条目。
type MulticaSkill struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MulticaState 连接诊断三层（CLI / 登录 / workspace）+ 远端库读回。
type MulticaState struct {
	OK          bool               `json:"ok"`
	WorkspaceID string             `json:"workspaceId"`
	Workspaces  []MulticaWorkspace `json:"workspaces"`
	CLI         struct {
		Found  bool   `json:"found"`
		Binary string `json:"binary"`
	} `json:"cli"`
	Auth struct {
		LoggedIn bool   `json:"loggedIn"`
		User     string `json:"user"`
		Server   string `json:"server"`
	} `json:"auth"`
	MCP struct {
		Servers []MulticaMCPServer   `json:"servers"`
		Error   *MulticaSectionError `json:"error"`
	} `json:"mcp"`
	Skills struct {
		Skills []MulticaSkill       `json:"skills"`
		Error  *MulticaSectionError `json:"error"`
	} `json:"skills"`
	Error *MulticaSectionError `json:"error"`
}

// MulticaState 读回 multica 连接状态（daemon 返回 200 结果体，失败也是诊断信息）。
func (c *Client) MulticaState() (*MulticaState, error) {
	var out MulticaState
	if err := c.do("GET", "/api/agents/multica/state", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SaveMulticaSettings 保存连接设置（binary/profile/serverUrl/workspaceId）。
func (c *Client) SaveMulticaSettings(fields map[string]any) error {
	return c.do("PUT", "/api/multica/settings", fields, nil)
}

// multicaStateLines 状态面板文本（诊断三层 + 远端库摘要）。
func multicaStateLines(st *MulticaState) string {
	lines := []string{}
	if st == nil {
		return "[red]未取到状态[-]"
	}
	if st.CLI.Found {
		lines = append(lines, "[green]✓ CLI[-] "+st.CLI.Binary)
	} else {
		lines = append(lines, "[red]✗ CLI 未找到[-] 安装 multica CLI，或在下方填入可执行文件路径")
	}
	if st.Auth.LoggedIn {
		lines = append(lines, "[green]✓ 已登录[-] "+orDash(st.Auth.User)+" @ "+orDash(st.Auth.Server))
	} else {
		lines = append(lines, "[yellow]! 未登录[-] 在终端运行 multica login 后点「刷新」")
	}
	if st.WorkspaceID != "" {
		lines = append(lines, "[green]✓ Workspace[-] "+st.WorkspaceID)
	}
	if st.Error != nil {
		lines = append(lines, fmt.Sprintf("[red]连接失败[-] %s %s", st.Error.Message, sectionHint(st.Error)))
	}
	lines = append(lines, "")
	if st.MCP.Error != nil {
		lines = append(lines, "[yellow]MCP 库读回失败[-] "+st.MCP.Error.Message)
	} else {
		names := make([]string, 0, len(st.MCP.Servers))
		for _, s := range st.MCP.Servers {
			names = append(names, s.Name)
		}
		lines = append(lines, fmt.Sprintf("[::b]MCP 库[-]（%d）%s", len(names), orDash(strings.Join(names, ", "))))
	}
	if st.Skills.Error != nil {
		lines = append(lines, "[yellow]Skill 库读回失败[-] "+st.Skills.Error.Message)
	} else {
		names := make([]string, 0, len(st.Skills.Skills))
		for _, s := range st.Skills.Skills {
			names = append(names, s.Name)
		}
		lines = append(lines, fmt.Sprintf("[::b]Skill 库[-]（%d）%s", len(names), orDash(strings.Join(names, ", "))))
	}
	lines = append(lines, "")
	lines = append(lines, "[gray]beaver 只把绑定/启用的条目收敛进库；库里的条目归哪些 Multica 智能体使用，由 Multica 自己决定。[-]")
	return strings.Join(lines, "\n")
}

func sectionHint(e *MulticaSectionError) string {
	if e == nil || e.Hint == "" {
		return ""
	}
	return "— " + e.Hint
}

// openMultica 打开「Multica 连接」模态：诊断 + workspace 切换（异步拉状态后排队渲染）。
func (p *syncPage) openMultica(a *App) {
	go func() {
		st, err := a.b.client.MulticaState()
		a.ui(func() {
			if err != nil {
				a.errd(err)
				return
			}
			p.renderMulticaModal(a, st)
		})
	}()
}

func (p *syncPage) renderMulticaModal(a *App, st *MulticaState) {
	view := tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	view.SetText(multicaStateLines(st))

	form := tview.NewForm()
	form.AddInputField("CLI 路径（空 = PATH）", st.CLI.Binary, 44, nil, nil) // item 0

	workspaceNames := make([]string, 0, len(st.Workspaces))
	workspaceIDs := make([]string, 0, len(st.Workspaces))
	sel := 0
	for i, w := range st.Workspaces {
		label := w.Name
		if w.Slug != "" {
			label += " (" + w.Slug + ")"
		}
		workspaceNames = append(workspaceNames, label)
		workspaceIDs = append(workspaceIDs, w.ID)
		if w.ID == st.WorkspaceID {
			sel = i
		}
	}
	if len(workspaceNames) == 0 {
		workspaceNames = []string{"（无可用 workspace）"}
		workspaceIDs = []string{""}
	}
	form.AddDropDown("Workspace", workspaceNames, sel, nil) // item 1

	form.AddButton("保存", func() {
		fields := map[string]any{
			"binary": strings.TrimSpace(form.GetFormItem(0).(*tview.InputField).GetText()),
		}
		if idx, _ := form.GetFormItem(1).(*tview.DropDown).GetCurrentOption(); idx >= 0 && idx < len(workspaceIDs) {
			if workspaceIDs[idx] != "" {
				fields["workspaceId"] = workspaceIDs[idx]
			}
		}
		go func() {
			if err := a.b.client.SaveMulticaSettings(fields); err != nil {
				a.errd(err)
				return
			}
			a.closeModalSafe()
			a.info("Multica 连接设置已保存")
			p.openMultica(a)
			p.load(a)
		}()
	})
	form.AddButton("刷新", func() { p.openMultica(a) })
	form.AddButton("关闭", func() { a.closeModal() })
	form.SetBorder(true).SetTitle(" Multica 连接与远端库 ").SetTitleAlign(tview.AlignLeft)

	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(view, 0, 1, false).
		AddItem(form, 8, 0, true)
	a.showModal(centered(body, 104, 26))
}
