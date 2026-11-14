package tui

// 概览页：daemon 运行状态、本机 agent 探测、运行环境与既有配置导入（对齐 Web StatusPage）。

import (
	"fmt"
	"strings"

	"github.com/rivo/tview"
)

type statusPage struct {
	statusView   *tview.TextView
	banner       *tview.TextView
	agentTable   *tview.Table
	envView      *tview.TextView
	importTable  *tview.Table
	importGroups []ImportGroup
}

func newStatusPage(a *App) *pageDef {
	p := &statusPage{
		statusView:  tview.NewTextView().SetDynamicColors(true).SetWrap(true),
		banner:      tview.NewTextView().SetDynamicColors(true).SetWrap(true),
		agentTable:  tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
		envView:     tview.NewTextView().SetDynamicColors(true).SetWrap(true),
		importTable: tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
	}

	p.agentTable.SetSelectedFunc(func(row, _ int) {
		if row >= 1 { // 与 Web 一致：点 agent 卡片跳「Agent 分发」
			go a.selectPage(3)
		}
	})

	// 导入表格：回车 = 导入选中行（循环内取指纹，异步确认）
	p.importTable.SetSelectedFunc(func(row, _ int) {
		idx := row - 1
		if idx < 0 || idx >= len(p.importGroups) {
			return
		}
		fp := p.importGroups[idx].Fingerprint
		name := p.importGroups[idx].Suggested.Name
		go func() {
			a.confirm("导入 MCP server",
				fmt.Sprintf("从现有 agent 配置导入「%s」？", name),
				func() { p.doImport(a, []string{fp}) })
		}()
	})

	buttons := tview.NewFlex()
	btnProbe := tview.NewButton("重新探测")
	btnProbe.SetSelectedFunc(func() { go a.refreshPage(0) })
	btnProbe.SetBorder(true)
	btnImportAll := tview.NewButton("全部导入")
	btnImportAll.SetSelectedFunc(func() {
		fps := []string{}
		for _, g := range p.importGroups { // p.importGroups 在循环内写，循环内读安全
			if !g.AlreadyImported {
				fps = append(fps, g.Fingerprint)
			}
		}
		go p.importAll(a, fps)
	})
	btnImportAll.SetBorder(true)
	buttons.AddItem(btnProbe, 0, 1, false).
		AddItem(btnImportAll, 0, 1, false).
		AddItem(nil, 0, 2, false)

	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(p.statusView, 0, 1, false).
		AddItem(p.banner, 0, 1, false).
		AddItem(sectionTitle("本机 Agent（回车查看详情）"), 1, 0, false).
		AddItem(p.agentTable, 0, 3, true).
		AddItem(sectionTitle("运行环境"), 1, 0, false).
		AddItem(p.envView, 0, 1, false).
		AddItem(sectionTitle("从现有 agent 配置导入（回车导入该项）"), 1, 0, false).
		AddItem(p.importTable, 0, 2, false).
		AddItem(buttons, 3, 0, false)

	prim := bordered(" 概览 ", content)

	return &pageDef{
		prim: prim,
		focus: p.agentTable,
		load: func(a *App) { p.load(a) },
	}
}

func sectionTitle(text string) tview.Primitive {
	tv := tview.NewTextView().SetDynamicColors(true)
	tv.SetText("[yellow:b]" + text + "[-:-]")
	return tv
}

func bordered(title string, inner tview.Primitive) tview.Primitive {
	f := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(inner, 0, 1, true)
	f.SetBorder(true).SetTitle(" " + title + " ").SetTitleAlign(tview.AlignLeft)
	return f
}

// load 同步拉取概览数据（status + agents + environment + import 预览）并套用。
func (p *statusPage) load(a *App) {
	status, stErr := a.b.client.Status()
	agents, agErr := a.b.client.Agents(false)
	tools, envErr := a.b.client.Environment()
	groups, imErr := a.b.client.ImportPreview()
	settings, setErr := a.b.client.Settings()
	p.importGroups = groups

	a.ui(func() {
		if stErr != nil {
			p.statusView.SetText("[red]状态获取失败: " + stErr.Error() + "[-]")
		} else {
			onboarded := setErr == nil && settings.HasOnboarded
			p.applyStatus(status, onboarded)
		}
		if agErr != nil {
			p.agentTable.Clear()
			p.agentTable.SetCell(0, 0, tview.NewTableCell("[red]agent 探测失败: "+agErr.Error()+"[-]").SetSelectable(false))
		} else {
			p.applyAgents(agents)
		}
		if envErr != nil {
			p.envView.SetText("[red]环境探测失败: " + envErr.Error() + "[-]")
		} else {
			p.applyEnv(tools)
		}
		if imErr != nil {
			p.importTable.Clear()
			p.importTable.SetCell(0, 0, tview.NewTableCell("[red]导入预览失败: "+imErr.Error()+"[-]").SetSelectable(false))
		} else {
			p.applyImport(groups)
		}
	})
}

func (p *statusPage) applyStatus(s *StatusInfo, onboarded bool) {
	gwState := "未运行"
	if s.Gateway.Running {
		gwState = fmt.Sprintf("运行中（端口 %d）", *s.Gateway.Port)
	}
	lines := []string{
		fmt.Sprintf("版本 [white]%s[-]   运行时长 [white]%s[-]   端口 [white]%d[-]", s.Version, humanDuration(s.UptimeSec), s.Port),
		fmt.Sprintf("数据目录 [white]%s[-]", s.HomeDir),
		fmt.Sprintf("MCP server [white]%d[-]   绑定 [white]%d[-]   分组 [white]%d[-]   网关 [white]%s[-]", s.ServerCount, s.BindingCount, s.GroupCount, gwState),
	}
	if s.ResyncPending {
		lines = append(lines, "[yellow]⚠ 有配置变更待重新分发（到「Agent 分发」执行同步）[-]")
	}
	p.statusView.SetText(strings.Join(lines, "\n"))

	if s.ServerCount+s.BindingCount == 0 && !onboarded {
		p.banner.SetText("[yellow:b]首次使用：[-][yellow]尚未注册任何 MCP server。可在「MCP 发现」检索收录，或从本机 agent 配置一键导入（下方）。「Agent 分发」负责写回各 agent 配置文件。[-]")
	} else {
		p.banner.SetText("")
	}
}

func (p *statusPage) applyAgents(agents []AgentInfo) {
	p.agentTable.Clear()
	p.agentTable.SetCell(0, 0, headerCell("状态"))
	p.agentTable.SetCell(0, 1, headerCell("Agent"))
	p.agentTable.SetCell(0, 2, headerCell("绑定 server"))
	p.agentTable.SetCell(0, 3, headerCell("配置文件"))
	for i, ag := range agents {
		mark, color := "○", "gray"
		if ag.Detected {
			mark, color = "●", "green"
		}
		row := i + 1
		p.agentTable.SetCell(row, 0, tview.NewTableCell(fmt.Sprintf("[%s]%s[-]", color, mark)))
		p.agentTable.SetCell(row, 1, tview.NewTableCell(ag.DisplayName))
		bound := ag.BoundServerIds
		if len(bound) == 0 {
			p.agentTable.SetCell(row, 2, tview.NewTableCell("[gray]（无）[-]"))
		} else {
			p.agentTable.SetCell(row, 2, tview.NewTableCell(strings.Join(bound, ", ")))
		}
		paths := strings.Join(ag.ConfigPaths, " 或 ")
		p.agentTable.SetCell(row, 3, tview.NewTableCell("[gray]"+paths+"[-]").SetMaxWidth(48))
	}
}

func (p *statusPage) applyEnv(tools []RuntimeTool) {
	var lines []string
	for _, t := range tools {
		mark, color := "✗", "red"
		if t.Found {
			mark, color = "✓", "green"
		}
		ver := ""
		if t.Version != nil {
			ver = " " + *t.Version
		}
		lines = append(lines, fmt.Sprintf("[%s]%s[-] %-8s %-14s%s", color, mark, t.Name, t.Role, ver))
	}
	if len(lines) == 0 {
		lines = append(lines, "[gray]（未检测到运行时工具）[-]")
	}
	p.envView.SetText(strings.Join(lines, "    "))
}

func (p *statusPage) applyImport(groups []ImportGroup) {
	p.importTable.Clear()
	p.importTable.SetCell(0, 0, headerCell("建议名称"))
	p.importTable.SetCell(0, 1, headerCell("传输"))
	p.importTable.SetCell(0, 2, headerCell("来源处"))
	n := 0
	for _, g := range groups {
		if g.AlreadyImported {
			continue
		}
		row := n + 1
		n++
		p.importTable.SetCell(row, 0, tview.NewTableCell(g.Suggested.Name))
		p.importTable.SetCell(row, 1, tview.NewTableCell(g.Suggested.Transport))
		p.importTable.SetCell(row, 2, tview.NewTableCell(fmt.Sprintf("%d 处", len(g.Candidates))))
	}
	if n == 0 {
		p.importTable.SetCell(1, 0, tview.NewTableCell("[gray]（没有可导入的新 server）[-]").SetSelectable(false))
	}
}

// importAll 全部导入确认（fps 已在事件循环内采集）。
func (p *statusPage) importAll(a *App, fps []string) {
	if len(fps) == 0 {
		a.info("没有可导入的新 server")
		return
	}
	a.confirm("导入 MCP server", fmt.Sprintf("从现有 agent 配置导入全部 %d 项？", len(fps)), func() {
		p.doImport(a, fps)
	})
}

func (p *statusPage) doImport(a *App, fps []string) {
	n, err := a.b.client.Import(fps)
	if err != nil {
		a.errd(err)
		return
	}
	a.info(fmt.Sprintf("已导入 %d 个 server", n))
	// 与 Web 向导"完成"等价：完成首次导入后不再显示首启横幅
	_ = a.b.client.PatchSettings(map[string]any{"hasOnboarded": true})
	a.refreshCurrent()
}

func headerCell(text string) *tview.TableCell {
	return tview.NewTableCell("[::b]" + text + "[-:-:b]").SetSelectable(false).SetExpansion(1)
}

func humanDuration(sec int) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm%ds", sec/60, sec%60)
	default:
		return fmt.Sprintf("%dh%dm", sec/3600, sec%60/60)
	}
}
