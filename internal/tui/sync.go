package tui

// Agent 分发页：分组管理、直连绑定与同步执行（对齐 Web SyncPage）。
// 冲突处置：报告含 conflicts 时弹出逐项 override/skip 选择，携带 resolutions 重新提交。

import (
	"fmt"
	"strings"

	"github.com/rivo/tview"

	"github.com/hxgh776/beaver/internal/store"
	"github.com/hxgh776/beaver/internal/syncengine"
)

// groupAgentTypes 与 daemon parseGroupInput 同款（zcode/workbuddy 不在分组可选列表）。
var groupAgentTypes = []string{
	store.AgentClaudeCode, store.AgentCodex, store.AgentCursor, store.AgentTrae,
	store.AgentOpencode, store.AgentClaudeDesktop, store.AgentWindsurf, store.AgentGeminiCLI,
}

type syncPage struct {
	servers    []*store.ServerDef
	agents     []AgentInfo
	groups     []store.ServerGroup
	groupTable *tview.Table
	bindTable  *tview.Table
	reportView *tview.TextView
	report     *syncengine.Report
}

func newSyncPage(a *App) *pageDef {
	p := &syncPage{
		groupTable: tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
		bindTable:  tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
		reportView: tview.NewTextView().SetDynamicColors(true).SetWrap(true),
	}

	groupToolbar := tview.NewFlex()
	newBtn := toolbarButton("新建分组", func() { go p.openGroupForm(a, nil) })
	editBtn := toolbarButton("编辑成员", func() {
		g := p.selectedGroup()
		go p.openGroupForm(a, g)
	})
	delBtn := toolbarButton("删除分组", func() {
		g := p.selectedGroup()
		go p.removeGroup(a, g)
	})
	groupToolbar.AddItem(newBtn, 14, 0, true).AddItem(editBtn, 14, 0, false).
		AddItem(delBtn, 14, 0, false).AddItem(nil, 0, 1, false)

	p.groupTable.SetSelectedFunc(func(row, _ int) {
		if row >= 1 {
			g := p.selectedGroup()
			go p.openGroupForm(a, g)
		}
	})
	left := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(sectionTitle("分组（回车编辑成员/改名）"), 1, 0, false).
		AddItem(p.groupTable, 0, 1, true).
		AddItem(groupToolbar, 3, 0, false)

	bindToolbar := tview.NewFlex()
	bindBtn := toolbarButton("添加直连绑定", func() { go p.openBindingForm(a, true) })
	unbindBtn := toolbarButton("移除直连绑定", func() { go p.openBindingForm(a, false) })
	bindToolbar.AddItem(bindBtn, 18, 0, true).AddItem(unbindBtn, 18, 0, false).AddItem(nil, 0, 1, false)

	p.bindTable.SetSelectedFunc(func(row, _ int) {
		if row >= 1 { // 回车同「添加直连绑定」
			go p.openBindingForm(a, true)
		}
	})
	right := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(sectionTitle("直连绑定（agent × server）"), 1, 0, false).
		AddItem(p.bindTable, 0, 1, true).
		AddItem(bindToolbar, 3, 0, false)

	syncToolbar := tview.NewFlex()
	dryBtn := toolbarButton("预演（dry-run）", func() { go p.runSync(a, true) })
	syncBtn := toolbarButton("立即同步", func() { go p.runSync(a, false) })
	syncToolbar.AddItem(dryBtn, 20, 0, true).AddItem(syncBtn, 14, 0, false).AddItem(nil, 0, 1, false)

	body := tview.NewFlex().
		AddItem(left, 0, 1, true).
		AddItem(right, 0, 1, false)
	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(body, 0, 2, true).
		AddItem(syncToolbar, 3, 0, false).
		AddItem(sectionTitle("同步报告"), 1, 0, false).
		AddItem(p.reportView, 0, 1, false)

	return &pageDef{
		prim: bordered(" Agent 分发 ", content),
		focus: p.groupTable,
		load: func(a *App) { p.load(a) },
		inst: p,
	}
}

func (p *syncPage) selectedGroup() *store.ServerGroup {
	row, _ := p.groupTable.GetSelection()
	if row <= 0 || row-1 >= len(p.groups) {
		return nil
	}
	return &p.groups[row-1]
}

func (p *syncPage) load(a *App) {
	servers, sErr := a.b.client.Servers()
	agents, aErr := a.b.client.Agents(false)
	groups, gErr := a.b.client.Groups()
	a.ui(func() {
		if sErr != nil || aErr != nil || gErr != nil {
			p.reportView.SetText("[red]数据加载失败[-]")
			return
		}
		p.servers = servers
		p.agents = agents
		p.groups = groups
		p.applyGroups()
		p.applyBindings()
	})
}

func (p *syncPage) applyGroups() {
	p.groupTable.Clear()
	p.groupTable.SetCell(0, 0, headerCell("分组"))
	p.groupTable.SetCell(0, 1, headerCell("包含 server"))
	p.groupTable.SetCell(0, 2, headerCell("关联 agent"))
	for i, g := range p.groups {
		row := i + 1
		p.groupTable.SetCell(row, 0, tview.NewTableCell(g.Name))
		p.groupTable.SetCell(row, 1, tview.NewTableCell(fmt.Sprintf("%d 个（%s）", len(g.ServerIDs), strings.Join(g.ServerIDs, ", "))).SetMaxWidth(30))
		p.groupTable.SetCell(row, 2, tview.NewTableCell(strings.Join(g.AgentTypes, ", ")).SetMaxWidth(30))
	}
	if len(p.groups) == 0 {
		p.groupTable.SetCell(1, 0, tview.NewTableCell("[gray]（无分组——分组可让多个 agent 共享同一组 server）[-]").SetSelectable(false))
	}
}

func (p *syncPage) applyBindings() {
	p.bindTable.Clear()
	p.bindTable.SetCell(0, 0, headerCell("Agent"))
	p.bindTable.SetCell(0, 1, headerCell("直连 server"))
	for i, ag := range p.agents {
		row := i + 1
		p.bindTable.SetCell(row, 0, tview.NewTableCell(ag.DisplayName))
		if len(ag.DirectServerIds) == 0 {
			p.bindTable.SetCell(row, 1, tview.NewTableCell("[gray]（无）[-]"))
		} else {
			p.bindTable.SetCell(row, 1, tview.NewTableCell(strings.Join(ag.DirectServerIds, ", ")).SetMaxWidth(40))
		}
	}
}

// openGroupForm 分组新建/编辑：复选框列出 server 与 agent，保存时按勾选整体保存成员。
// 部件操作经 ui 排队；可在任意 goroutine 调用。
func (p *syncPage) openGroupForm(a *App, g *store.ServerGroup) {
	a.ui(func() {
		form := tview.NewForm()
		form.AddInputField("分组名称", groupName(g), 20, nil, nil) // item 0

		serverOrder := make([]string, 0, len(p.servers))
		for _, s := range p.servers {
			form.AddCheckbox("server: "+s.ID, g != nil && containsStr(g.ServerIDs, s.ID), nil)
			serverOrder = append(serverOrder, s.ID)
		}
		agentOrder := make([]string, 0, len(groupAgentTypes))
		for _, at := range groupAgentTypes {
			form.AddCheckbox("agent: "+at, g != nil && containsStr(g.AgentTypes, at), nil)
			agentOrder = append(agentOrder, at)
		}

		form.AddButton("保存", func() {
			nameField := form.GetFormItem(0).(*tview.InputField)
			name := nameField.GetText()
			if name == "" {
				go a.info("分组名称不能为空")
				return
			}
			var wantServers, wantAgents []string
			for i, id := range serverOrder {
				if form.GetFormItem(1+i).(*tview.Checkbox).IsChecked() {
					wantServers = append(wantServers, id)
				}
			}
			for i, at := range agentOrder {
				if form.GetFormItem(1+len(serverOrder)+i).(*tview.Checkbox).IsChecked() {
					wantAgents = append(wantAgents, at)
				}
			}
			go p.saveGroup(a, g, name, wantServers, wantAgents)
		})
		// 事件循环内直接关模态
		form.AddButton("取消", func() { a.closeModal() })
		title := "新建分组"
		if g != nil {
			title = "编辑分组 — " + g.ID
		}
		form.SetBorder(true).SetTitle(" " + title + " ").SetTitleAlign(tview.AlignLeft)
		a.showModal(centered(form, 84, 38))
	})
}

func groupName(g *store.ServerGroup) string {
	if g == nil {
		return ""
	}
	return g.Name
}

// saveGroup 保存分组（异步 goroutine 调用）。
func (p *syncPage) saveGroup(a *App, g *store.ServerGroup, name string, wantServers, wantAgents []string) {
	if err := a.b.client.SaveGroup(groupID(g), name, wantServers, wantAgents); err != nil {
		a.errd(err)
		return
	}
	// PATCH 已整体覆盖成员；直连绑定不属于分组，无需额外处理
	a.closeModalSafe()
	a.info("分组已保存")
	p.load(a)
}

func groupID(g *store.ServerGroup) string {
	if g == nil {
		return ""
	}
	return g.ID
}

// removeGroup 删除分组（g 已在事件循环内捕获）。
func (p *syncPage) removeGroup(a *App, g *store.ServerGroup) {
	if g == nil {
		a.info("先选中一个分组")
		return
	}
	a.confirm("删除分组", fmt.Sprintf("删除分组「%s」？（不删除 server 本身）", g.Name), func() {
		if err := a.b.client.DeleteGroup(g.ID); err != nil {
			a.errd(err)
			return
		}
		a.info("分组已删除")
		p.load(a)
	})
}

// openBindingForm 直连绑定增删：选 agent 与 server 后 PUT/DELETE /api/bindings。
// 部件操作经 ui 排队；可在任意 goroutine 调用。
func (p *syncPage) openBindingForm(a *App, bind bool) {
	a.ui(func() {
		action := "移除"
		if bind {
			action = "添加"
		}
		agentNames := []string{}
		agentValues := []string{}
		for _, ag := range p.agents {
			agentNames = append(agentNames, ag.DisplayName)
			agentValues = append(agentValues, ag.AgentType)
		}
		serverNames := []string{}
		serverValues := []string{}
		for _, s := range p.servers {
			serverNames = append(serverNames, s.ID)
			serverValues = append(serverValues, s.ID)
		}
		if len(agentValues) == 0 || len(serverValues) == 0 {
			a.info("需要已检测的 agent 与已注册的 server")
			return
		}
		form := tview.NewForm()
		form.AddDropDown("Agent", agentNames, 0, nil)   // item 0
		form.AddDropDown("Server", serverNames, 0, nil) // item 1
		form.AddButton(action, func() {
			ai, _ := form.GetFormItem(0).(*tview.DropDown).GetCurrentOption()
			si, _ := form.GetFormItem(1).(*tview.DropDown).GetCurrentOption()
			agentType := agentValues[ai]
			serverID := serverValues[si]
			go func() {
				if err := a.b.client.Binding(agentType, serverID, bind); err != nil {
					a.errd(err)
					return
				}
				a.closeModalSafe()
				a.info(fmt.Sprintf("已%s直连绑定 %s × %s", action, agentType, serverID))
				p.load(a)
			}()
		})
		form.AddButton("取消", func() { a.closeModal() })
		form.SetBorder(true).SetTitle(" " + action + "直连绑定 ").SetTitleAlign(tview.AlignLeft)
		a.showModal(centered(form, 70, 9))
	})
}

// runSync 执行同步；冲突时弹逐项处置（override/skip），带 resolutions 重提。
func (p *syncPage) runSync(a *App, dryRun bool) {
	go func() {
		report, err := a.b.client.Sync(dryRun, nil)
		a.ui(func() {
			if err != nil {
				a.errd(err)
				return
			}
			conflicts := collectConflicts(report)
			if len(conflicts) > 0 {
				p.resolveConflicts(a, conflicts, dryRun)
				return
			}
			p.applyReport(report, dryRun)
			if dryRun {
				a.info("预演完成（未落盘）")
			} else {
				a.info("同步完成")
			}
		})
	}()
}

type conflictItem struct {
	agentType string
	key       string
	resKey    string
	current   string
}

func collectConflicts(report *syncengine.Report) []conflictItem {
	var out []conflictItem
	for _, per := range report.PerAgent {
		for _, c := range per.Conflicts {
			out = append(out, conflictItem{
				agentType: per.AgentType,
				key:       c.Key,
				resKey:    c.ResolutionKey,
				current:   c.CurrentHash,
			})
		}
	}
	return out
}

// resolveConflicts 冲突处置：逐项 override（覆盖本地改动）/ skip（跳过该项）。
func (p *syncPage) resolveConflicts(a *App, conflicts []conflictItem, dryRun bool) {
	form := tview.NewForm()
	resolutions := map[string]string{}
	form.AddTextView("", "检测到本地配置与同步目标不一致的条目：", 60, 1, true, false)
	for _, c := range conflicts {
		c := c
		form.AddDropDown(fmt.Sprintf("%s · %s", c.agentType, c.key), []string{"skip（保留本地）", "override（覆盖本地）"}, 0, func(option string, _ int) {
			if strings.HasPrefix(option, "override") {
				resolutions[c.resKey] = "override"
			} else {
				resolutions[c.resKey] = "skip"
			}
		})
	}
	form.AddButton("继续同步", func() {
		a.closeModal()
		go func() {
			report, err := a.b.client.Sync(dryRun, resolutions)
			a.ui(func() {
				if err != nil {
					a.errd(err)
					return
				}
				p.applyReport(report, dryRun)
				if dryRun {
					a.info("预演完成（未落盘）")
				} else {
					a.info("同步完成（冲突已按所选方式处置）")
				}
			})
		}()
	})
	form.AddButton("取消", func() { a.closeModal() })
	form.SetBorder(true).SetTitle(" 冲突处置 ").SetTitleAlign(tview.AlignLeft)
	a.showModal(centered(form, 100, 12+len(conflicts)*1))
}

func (p *syncPage) applyReport(report *syncengine.Report, dryRun bool) {
	p.report = report
	var lines []string
	if dryRun {
		lines = append(lines, "[yellow:b]预演报告（未写盘）[-]")
	} else {
		lines = append(lines, "[green:b]同步报告[-]")
	}
	for _, per := range report.PerAgent {
		active := 0
		var sub []string
		for _, ch := range per.Changes {
			if ch.Action == "none" {
				continue
			}
			active++
			sub = append(sub, fmt.Sprintf("  [cyan]%s[-] %s", ch.Action, ch.Key))
		}
		for _, cf := range per.Conflicts {
			sub = append(sub, fmt.Sprintf("  [red]⚠ 冲突 %s[-]（处置：%s）", cf.Key, string(cf.Resolution)))
		}
		for _, u := range per.Unsupported {
			sub = append(sub, fmt.Sprintf("  [yellow]! 跳过 %s: %s[-]", u.ServerID, u.Reason))
		}
		if active == 0 && len(sub) == 0 {
			continue
		}
		lines = append(lines, "[::b]"+per.AgentType+"[-]  "+orDash(per.File))
		lines = append(lines, sub...)
	}
	if len(lines) == 1 {
		lines = append(lines, "[gray]各 agent 已是最新，无变更。[-]")
	}
	p.reportView.SetText(strings.Join(lines, "\n"))
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
