package tui

// MCP 管理页：注册表增删改查、启停、工具开关与调试调用、上游进程控制（对齐 Web ServersPage）。
//
// 并发约定（全页统一，见 app.go ui 注释）：
//   - tview 回调（按钮/表格回车/表单保存）运行在事件循环 goroutine：只做部件读取，
//     随后 go 异步化其余工作；
//   - 异步函数内所有部件操作经 a.ui() 排队到循环（阻塞至完成），client 调用直接执行。

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/hxgh776/beaver/internal/store"
)

type serversPage struct {
	table    *tview.Table
	envWarn  *tview.TextView
	servers  []*store.ServerDef
	envTools []RuntimeTool
	upstream map[string]string // serverID → 上游状态
}

// serverTemplates 与 Web 内置模板对应的常用收录模板（新增时一键填充表单）。
var serverTemplates = []struct {
	name      string
	transport string
	command   string
	args      []string
	url       string
	gateway   bool
}{
	{"空白（手填）", "", "", nil, "", true},
	{"context7（HTTP）", "http", "", nil, "https://mcp.context7.com/mcp", true},
	{"filesystem（stdio·npx）", "stdio", "npx", []string{"-y", "@modelcontextprotocol/server-filesystem", "."}, "", true},
	{"fetch（stdio·uvx）", "stdio", "uvx", []string{"mcp-server-fetch"}, "", true},
	{"memory（stdio·npx）", "stdio", "npx", []string{"-y", "@modelcontextprotocol/server-memory"}, "", true},
	{"sequential-thinking（stdio·npx）", "stdio", "npx", []string{"-y", "@modelcontextprotocol/server-sequential-thinking"}, "", true},
}

func newServersPage(a *App) *pageDef {
	p := &serversPage{
		table:   tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
		envWarn: tview.NewTextView().SetDynamicColors(true).SetWrap(true),
	}

	toolbar := tview.NewFlex()
	addBtn := toolbarButton("新增", func() { go p.openTemplates(a) })
	editBtn := toolbarButton("编辑", func() {
		if s := p.selected(); s != nil {
			go p.openEdit(a, s)
		}
	})
	delBtn := toolbarButton("删除", func() {
		s := p.selected()
		go p.remove(a, s)
	})
	toggleBtn := toolbarButton("启用/停用", func() {
		s := p.selected()
		go p.toggle(a, s)
	})
	toolsBtn := toolbarButton("工具/调试", func() {
		if s := p.selected(); s != nil {
			go p.openTools(a, s)
		}
	})
	toolbar.AddItem(addBtn, 12, 0, true).AddItem(editBtn, 12, 0, false).
		AddItem(delBtn, 12, 0, false).AddItem(toggleBtn, 16, 0, false).
		AddItem(toolsBtn, 16, 0, false).AddItem(nil, 0, 1, false)

	p.table.SetSelectedFunc(func(row, _ int) {
		if row >= 1 && row-1 < len(p.servers) { // 回车 = 编辑（指针在循环内取，安全）
			s := p.servers[row-1]
			go p.openEdit(a, s)
		}
	})
	// 空表回车 = 新增；Delete 键 = 删除选中行
	p.table.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEnter && len(p.servers) == 0 {
			go p.openTemplates(a)
			return nil
		}
		if ev.Key() == tcell.KeyDelete && len(p.servers) > 0 {
			s := p.selected()
			go p.remove(a, s)
			return nil
		}
		return ev
	})

	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(p.envWarn, 1, 0, false).
		AddItem(toolbar, 3, 0, false).
		AddItem(p.table, 0, 1, true)

	return &pageDef{
		prim:  bordered(" MCP 管理（回车编辑/新增 · Delete 删除 · 工具调试） ", content),
		focus: p.table,
		load:  func(a *App) { p.load(a) },
	}
}

// toolbarButton 工具栏按钮（fn 在事件循环内执行：只读部件 + go 异步化）。
func toolbarButton(label string, fn func()) *tview.Button {
	b := tview.NewButton(label)
	b.SetSelectedFunc(fn)
	b.SetBorder(true)
	return b
}

func (p *serversPage) selected() *store.ServerDef {
	row, _ := p.table.GetSelection()
	if row <= 0 || row-1 >= len(p.servers) {
		return nil
	}
	return p.servers[row-1]
}

func (p *serversPage) load(a *App) {
	servers, err := a.b.client.Servers()
	gw, _ := a.b.client.Gateway()
	tools, _ := a.b.client.Environment()
	upstream := map[string]string{}
	if gw != nil {
		for _, u := range gw.Upstreams {
			upstream[u.ID] = u.Status
		}
	}
	envTools := tools
	a.ui(func() {
		if err != nil {
			p.table.Clear()
			p.table.SetCell(0, 0, tview.NewTableCell("[red]加载失败: "+err.Error()+"[-]").SetSelectable(false))
			return
		}
		p.servers = servers
		p.upstream = upstream
		p.envTools = envTools
		p.apply()
	})
}

func (p *serversPage) apply() {
	p.table.Clear()
	p.table.SetCell(0, 0, headerCell("状态"))
	p.table.SetCell(0, 1, headerCell("名称"))
	p.table.SetCell(0, 2, headerCell("ID"))
	p.table.SetCell(0, 3, headerCell("传输"))
	p.table.SetCell(0, 4, headerCell("模式"))
	p.table.SetCell(0, 5, headerCell("上游"))
	p.table.SetCell(0, 6, headerCell("目标"))
	p.envWarn.SetText(p.envWarning())

	for i, s := range p.servers {
		row := i + 1
		state, color := "○", "gray"
		if s.Enabled {
			state, color = "●", "green"
		}
		p.table.SetCell(row, 0, tview.NewTableCell(fmt.Sprintf("[%s]%s[-]", color, state)))
		p.table.SetCell(row, 1, tview.NewTableCell(s.Name))
		p.table.SetCell(row, 2, tview.NewTableCell("[gray]"+s.ID+"[-]"))
		p.table.SetCell(row, 3, tview.NewTableCell(string(s.Transport)))
		mode := "直连"
		if s.GatewayMode {
			mode = "网关"
		}
		p.table.SetCell(row, 4, tview.NewTableCell(mode))
		if s.GatewayMode {
			st := p.upstream[s.ID]
			cellText, cellColor := "—", "gray"
			switch st {
			case "ready":
				cellText, cellColor = "ready", "green"
			case "starting":
				cellText, cellColor = "starting", "yellow"
			case "error", "backoff":
				cellText, cellColor = st, "red"
			}
			p.table.SetCell(row, 5, tview.NewTableCell(fmt.Sprintf("[%s]%s[-]", cellColor, cellText)))
		} else {
			p.table.SetCell(row, 5, tview.NewTableCell("[gray]—[-]"))
		}
		target := ""
		if s.Command != nil {
			target = *s.Command + " " + strings.Join(derefArgs(s.Args), " ")
		}
		if s.URL != nil {
			target = *s.URL
		}
		p.table.SetCell(row, 6, tview.NewTableCell("[gray]"+target+"[-]").SetMaxWidth(46))
	}
}

func derefArgs(args *[]string) []string {
	if args == nil {
		return nil
	}
	return *args
}

// envWarning stdio 命令依赖 node/npx/uvx/docker 但运行时缺失时的提示（对齐 Web）。
func (p *serversPage) envWarning() string {
	if len(p.servers) == 0 {
		return ""
	}
	missing := map[string]bool{}
	for _, t := range p.envTools {
		if !t.Found {
			missing[t.Name] = true
		}
	}
	needed := map[string]bool{}
	for _, s := range p.servers {
		if s.Transport != store.TransportStdio || s.Command == nil || !s.Enabled {
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(*s.Command), ".exe"), ".cmd")
		if missing[base] {
			needed[base] = true
		}
	}
	if len(needed) == 0 {
		return ""
	}
	names := make([]string, 0, len(needed))
	for k := range needed {
		names = append(names, k)
	}
	sort.Strings(names)
	return "[red]⚠ stdio server 依赖的运行时缺失: " + strings.Join(names, ", ") + "[-]"
}

// openTemplates 新增前的模板选择（等价 Web 模板网格）。
func (p *serversPage) openTemplates(a *App) {
	list := tview.NewList()
	a.ui(func() {
		for i := range serverTemplates {
			tpl := &serverTemplates[i]
			list.AddItem(tpl.name, "", 0, func() {
				go func() {
					a.closeModalSafe()
					p.openEditWithTemplate(a, nil, tpl)
				}()
			})
		}
		a.showModal(centered(bordered(" 选择模板 ", list), 72, 16))
	})
}

func (p *serversPage) openEdit(a *App, s *store.ServerDef) {
	p.openEditWithTemplate(a, s, nil)
}

// openEditWithTemplate 新增（s=nil）/编辑共用表单；tpl 非空时作为新增初值。
// 部件操作经 ui 排队；可在任意 goroutine 调用。
func (p *serversPage) openEditWithTemplate(a *App, s *store.ServerDef, tpl *struct {
	name      string
	transport string
	command   string
	args      []string
	url       string
	gateway   bool
}) {
	a.ui(func() {
		form := tview.NewForm()

		name0, transport0, command0, cwd0, url0, gateway0, enabled0, concurrency0 := "", "stdio", "", "", "", true, true, ""
		var args0, env0, headers0 []string
		if s != nil {
			name0 = s.Name
			transport0 = string(s.Transport)
			command0 = derefStr(s.Command)
			cwd0 = derefStr(s.Cwd)
			url0 = derefStr(s.URL)
			gateway0 = s.GatewayMode
			enabled0 = s.Enabled
			if s.Concurrency != nil {
				concurrency0 = fmt.Sprint(*s.Concurrency)
			}
			if s.Args != nil {
				args0 = *s.Args
			}
			if s.Env != nil {
				for k, v := range *s.Env {
					env0 = append(env0, k+"="+v)
				}
				sort.Strings(env0)
			}
			if s.Headers != nil {
				for k, v := range *s.Headers {
					headers0 = append(headers0, k+"="+v)
				}
				sort.Strings(headers0)
			}
		} else if tpl != nil {
			transport0 = tpl.transport
			command0 = tpl.command
			url0 = tpl.url
			gateway0 = tpl.gateway
			args0 = tpl.args
		}

		// 字段顺序：可 Tab 直达按钮的输入项在前；TextArea 会吞 Tab，放末尾并在表单层接管
		form.AddInputField("ID（留空自动生成）", derefID(s), 24, nil, nil)  // item 0
		form.AddInputField("名称 *", name0, 24, nil, nil)                  // item 1
		form.AddDropDown("传输类型 *", []string{"stdio", "http", "sse"}, indexOf([]string{"stdio", "http", "sse"}, transport0), nil) // item 2
		form.AddInputField("命令（stdio）", command0, 32, nil, nil)          // item 3
		form.AddInputField("工作目录", cwd0, 32, nil, nil)                   // item 4
		form.AddInputField("URL（http/sse）", url0, 40, nil, nil)           // item 5
		form.AddCheckbox("网关模式（聚合端点统一分发）", gateway0, nil)        // item 6
		form.AddCheckbox("启用", enabled0, nil)                            // item 7
		form.AddInputField("并发数（1-16，可空）", concurrency0, 8, nil, nil)   // item 8
		form.AddTextArea("参数·每行一条（Tab 跳到保存）", strings.Join(args0, "\n"), 40, 2, 0, nil) // item 9
		form.AddTextArea("环境变量·每行 KEY=值（Tab 跳到保存）", strings.Join(env0, "\n"), 40, 2, 0, nil) // item 10
		form.AddTextArea("请求头·每行 KEY=值（Tab 跳到保存）", strings.Join(headers0, "\n"), 40, 2, 0, nil) // item 11
		multiAreas := []*tview.TextArea{
			form.GetFormItem(9).(*tview.TextArea),
			form.GetFormItem(10).(*tview.TextArea),
			form.GetFormItem(11).(*tview.TextArea),
		}

		form.AddButton("保存", func() { p.saveForm(a, s, form) })
		form.AddButton("取消", func() { a.closeModalSafe() })
		// TextArea 自己吃掉 Tab；在表单层拦截：多行框内按 Tab 直接跳到「保存」按钮
		form.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
			if ev.Key() != tcell.KeyTab {
				return ev
			}
			for _, ta := range multiAreas {
				if ta.HasFocus() {
					a.tapp.SetFocus(form.GetButton(0))
					return nil
				}
			}
			return ev
		})
		form.SetBorder(true).SetTitle(" " + formTitle(s) + " ").SetTitleAlign(tview.AlignLeft)
		a.showModal(centered(form, 86, 38))
	})
}

func formTitle(s *store.ServerDef) string {
	if s == nil {
		return "新增 MCP server"
	}
	return "编辑 MCP server — " + s.ID
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefID(s *store.ServerDef) string {
	if s == nil {
		return ""
	}
	return s.ID
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return 0
}

// saveForm 保存按钮回调（事件循环内）：读取表单值后在独立 goroutine 提交。
func (p *serversPage) saveForm(a *App, s *store.ServerDef, form *tview.Form) {
	// 表单项按下标读取（顺序见 openEditWithTemplate）；部件读取在循环内，安全
	idField := form.GetFormItem(0).(*tview.InputField)
	nameField := form.GetFormItem(1).(*tview.InputField)
	transportField := form.GetFormItem(2).(*tview.DropDown)
	commandField := form.GetFormItem(3).(*tview.InputField)
	cwdField := form.GetFormItem(4).(*tview.InputField)
	urlField := form.GetFormItem(5).(*tview.InputField)
	gatewayCheck := form.GetFormItem(6).(*tview.Checkbox)
	enabledCheck := form.GetFormItem(7).(*tview.Checkbox)
	concField := form.GetFormItem(8).(*tview.InputField)
	argsArea := form.GetFormItem(9).(*tview.TextArea)
	envArea := form.GetFormItem(10).(*tview.TextArea)
	headersArea := form.GetFormItem(11).(*tview.TextArea)

	_, transport := transportField.GetCurrentOption()
	id := strings.TrimSpace(idField.GetText())
	name := nameField.GetText()
	command := strings.TrimSpace(commandField.GetText())
	args := linesNonEmpty(argsArea.GetText())
	cwd := strings.TrimSpace(cwdField.GetText())
	url := strings.TrimSpace(urlField.GetText())
	env := kvMap(envArea.GetText())
	headers := kvMap(headersArea.GetText())
	gatewayMode := gatewayCheck.IsChecked()
	enabled := enabledCheck.IsChecked()
	conc := strings.TrimSpace(concField.GetText())

	go func() {
		body := map[string]any{
			"name":        name,
			"transport":   transport,
			"command":     command,
			"args":        args,
			"cwd":         cwd,
			"url":         url,
			"env":         env,
			"headers":     headers,
			"gatewayMode": gatewayMode,
			"enabled":     enabled,
		}
		if id != "" {
			body["id"] = id
		}
		if conc != "" {
			var n int
			if _, err := fmt.Sscanf(conc, "%d", &n); err == nil {
				body["concurrency"] = n
			}
		}
		// 空串字段按缺省语义剔除（nil = 缺省，避免覆盖为空值）
		for _, key := range []string{"command", "cwd", "url"} {
			if v, ok := body[key].(string); ok && v == "" {
				delete(body, key)
			}
		}
		if len(body["args"].([]string)) == 0 {
			delete(body, "args")
		}
		if len(body["env"].(map[string]string)) == 0 {
			delete(body, "env")
		}
		if len(body["headers"].(map[string]string)) == 0 {
			delete(body, "headers")
		}

		var err error
		if s != nil {
			_, err = a.b.client.PatchServer(s.ID, body)
		} else {
			_, err = a.b.client.CreateServer(body)
		}
		if err != nil {
			a.errd(err)
			return
		}
		a.closeModalSafe()
		if s != nil {
			a.info("已更新 " + s.ID)
		} else {
			a.info("已添加")
		}
		p.load(a)
	}()
}

// linesNonEmpty 多行文本 → 非空行列表（args 用）。
func linesNonEmpty(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// kvMap "K=V" 行 → map（env/headers 用）。
func kvMap(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i := strings.Index(line, "="); i > 0 {
			out[line[:i]] = line[i+1:]
		}
	}
	return out
}

func (p *serversPage) remove(a *App, s *store.ServerDef) {
	if s == nil {
		a.info("先选中一个 server")
		return
	}
	a.confirm("删除 MCP server", fmt.Sprintf("删除「%s」（%s）？绑定与分组引用将一并清理。", s.Name, s.ID), func() {
		if err := a.b.client.DeleteServer(s.ID); err != nil {
			a.errd(err)
			return
		}
		a.info("已删除 " + s.ID)
		p.load(a)
	})
}

func (p *serversPage) toggle(a *App, s *store.ServerDef) {
	if s == nil {
		a.info("先选中一个 server")
		return
	}
	_, err := a.b.client.PatchServer(s.ID, map[string]any{"enabled": !s.Enabled})
	if err != nil {
		a.errd(err)
		return
	}
	a.info(fmt.Sprintf("%s 已%s", s.ID, enabledWord(!s.Enabled)))
	p.load(a)
}

// openTools 工具列表 + 开关 + 调试调用 + 上游进程控制。
func (p *serversPage) openTools(a *App, s *store.ServerDef) {
	if s == nil {
		a.info("先选中一个 server")
		return
	}
	view := &toolsView{page: p, server: s}
	a.ui(func() { view.build(a) })
}

type toolsView struct {
	page      *serversPage
	server    *store.ServerDef
	table     *tview.Table
	tools     []ToolInfo
	output    *tview.TextView
	container *tview.Flex
}

// build 组装工具/调试模态（调用方已保证在 ui 闭包内或事件循环内）。
func (v *toolsView) build(a *App) {
	v.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	v.output = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	v.table.SetSelectedFunc(func(row, _ int) {
		if row >= 1 && row-1 < len(v.tools) {
			t := v.tools[row-1]
			go v.invokeDialog(a, t)
		}
	})

	toolbar := tview.NewFlex()
	refreshBtn := toolbarButton("刷新工具列表", func() { go v.load(a, true) })
	toggleBtn := toolbarButton("启用/停用所选", func() {
		row, _ := v.table.GetSelection()
		if row >= 1 && row-1 < len(v.tools) {
			idx := row - 1
			go v.toggleTool(a, idx)
		}
	})
	invokeBtn := toolbarButton("调用所选", func() {
		row, _ := v.table.GetSelection()
		if row >= 1 && row-1 < len(v.tools) {
			t := v.tools[row-1]
			go v.invokeDialog(a, t)
		}
	})
	startBtn := toolbarButton("上游启动", func() { go v.control(a, "start") })
	stopBtn := toolbarButton("上游停止", func() { go v.control(a, "stop") })
	restartBtn := toolbarButton("上游重启", func() { go v.control(a, "restart") })
	toolbar.AddItem(refreshBtn, 20, 0, true).AddItem(toggleBtn, 20, 0, false).
		AddItem(invokeBtn, 14, 0, false).AddItem(startBtn, 14, 0, false).
		AddItem(stopBtn, 14, 0, false).AddItem(restartBtn, 14, 0, false).
		AddItem(nil, 0, 1, false)

	v.container = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(toolbar, 3, 0, false).
		AddItem(v.table, 0, 3, true).
		AddItem(sectionTitle("调试输出"), 1, 0, false).
		AddItem(v.output, 0, 2, false)

	a.showModal(centered(bordered(" 工具与调试 — "+v.server.ID+" ", v.container), 110, 34))
}

func (v *toolsView) load(a *App, refresh bool) {
	tools, err := a.b.client.ServerTools(v.server.ID, refresh)
	a.ui(func() {
		if err != nil {
			v.output.SetText("[red]工具列表获取失败: " + err.Error() + "[-]")
			v.table.Clear()
			return
		}
		v.tools = tools
		v.applyTools()
	})
}

func (v *toolsView) applyTools() {
	v.table.Clear()
	v.table.SetCell(0, 0, headerCell("工具"))
	v.table.SetCell(0, 1, headerCell("启用"))
	v.table.SetCell(0, 2, headerCell("说明"))
	for i, t := range v.tools {
		row := i + 1
		v.table.SetCell(row, 0, tview.NewTableCell(t.Name))
		mark, color := "✗", "red"
		if t.Enabled {
			mark, color = "✓", "green"
		}
		v.table.SetCell(row, 1, tview.NewTableCell(fmt.Sprintf("[%s]%s[-]", color, mark)))
		desc := ""
		if t.Description != nil {
			desc = *t.Description
		}
		v.table.SetCell(row, 2, tview.NewTableCell("[gray]"+desc+"[-]").SetMaxWidth(60))
	}
	if len(v.tools) == 0 {
		v.table.SetCell(1, 0, tview.NewTableCell("[gray]（上游未返回工具）[-]").SetSelectable(false))
	}
}

// toggleTool 工具开关（合并完整 toolOverrides 后 PATCH）。
func (v *toolsView) toggleTool(a *App, idx int) {
	t := v.tools[idx]
	overrides := map[string]store.ToolOverride{}
	if v.server.ToolOverrides != nil {
		for k, o := range *v.server.ToolOverrides {
			overrides[k] = o
		}
	}
	t.Enabled = !t.Enabled
	overrides[t.Name] = store.ToolOverride{Enabled: t.Enabled}
	if _, err := a.b.client.PatchServer(v.server.ID, map[string]any{"toolOverrides": overrides}); err != nil {
		a.errd(err)
		return
	}
	a.ui(func() {
		v.tools[idx] = t
		v.server.ToolOverrides = &overrides // 本地同步，避免下次合并丢失
		v.applyTools()
	})
	a.info(fmt.Sprintf("%s.%s 已%s", v.server.ID, t.Name, enabledWord(t.Enabled)))
}

func (v *toolsView) control(a *App, action string) {
	if err := a.b.client.UpstreamControl(v.server.ID, action); err != nil {
		a.errd(err)
		return
	}
	a.info("上游已" + map[string]string{"start": "启动", "stop": "停止", "restart": "重启"}[action])
	v.page.load(a)
}

// invokeDialog 调试调用：编辑 JSON 参数直接调用工具（不经 agent，等价 Web 调试台）。
func (v *toolsView) invokeDialog(a *App, t ToolInfo) {
	a.ui(func() {
		form := tview.NewForm()
		form.AddTextArea("参数（JSON）", "{}", 50, 4, 0, nil) // item 0
		result := tview.NewTextView().SetDynamicColors(true).SetWrap(true)
		form.AddTextView("", "结果", 0, 4, true, true) // item 1
		form.AddButton("调用 "+t.Name, func() {
			text := form.GetFormItem(0).(*tview.TextArea).GetText()
			go func() {
				args := map[string]any{}
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
						a.ui(func() { result.SetText("[red]参数 JSON 解析失败: " + err.Error() + "[-]") })
						return
					}
				}
				out, err := a.b.client.InvokeTool(v.server.ID, t.Name, args)
				a.ui(func() {
					if err != nil {
						result.SetText("[red]调用失败: " + err.Error() + "[-]")
						return
					}
					data, _ := json.MarshalIndent(out, "", "  ")
					result.SetText("[green]✓ 结果:[-]\n" + string(data))
				})
			}()
		})
		form.AddButton("关闭", func() { a.closeModalSafe() })
		form.SetBorder(true).SetTitle(" 调用 — " + v.server.ID + " · " + t.Name + " ").SetTitleAlign(tview.AlignLeft)
		a.showModal(centered(form, 92, 22))
	})
}

func enabledWord(on bool) string {
	if on {
		return "启用"
	}
	return "停用"
}
