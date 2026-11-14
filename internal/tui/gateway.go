package tui

// 聚合网关页：启停、上游进程状态与控制、访问令牌与鉴权开关（对齐 Web GatewayPage）。

import (
	"fmt"
	"strings"
	"time"

	"github.com/rivo/tview"
)

type gatewayPage struct {
	status    *StatusInfo
	gw        *GatewayInfo
	token     string
	tokenView *tview.TextView
	gwView    *tview.TextView
	upTable   *tview.Table
	epsView   *tview.TextView
	tokenShown bool
	// revealGen 令牌明文展示代次：轮换后的临时明文经 AfterFunc 定时回隐；
	// 手动切换或再次轮换代次递增，取消尚未触发的旧回隐。
	revealGen int
}

func newGatewayPage(a *App) *pageDef {
	p := &gatewayPage{
		gwView:  tview.NewTextView().SetDynamicColors(true).SetWrap(true),
		upTable: tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
		epsView: tview.NewTextView().SetDynamicColors(true).SetWrap(true),
		tokenView: tview.NewTextView().SetDynamicColors(true).SetWrap(true),
	}

	toolbar := tview.NewFlex()
	startBtn := toolbarButton("启动网关", func() { go p.control(a, "start") })
	stopBtn := toolbarButton("停止网关", func() { go p.control(a, "stop") })
	toggleAuth := toolbarButton("切换令牌鉴权", func() {
		authOn := p.status != nil && p.status.AuthRequired // 循环内读，安全
		go p.toggleAuth(a, authOn)
	})
	rotateBtn := toolbarButton("轮换令牌", func() { go p.rotateToken(a) })
	revealBtn := toolbarButton("显示/隐藏令牌", func() {
		// 循环内直接翻转并重渲染（字段只在事件循环内读写，消除竞态）；
		// 代次递增同时取消轮换令牌遗留的自动回隐定时器
		p.revealGen++
		p.tokenShown = !p.tokenShown
		p.applyToken(a, nil)
	})
	toolbar.AddItem(startBtn, 14, 0, true).AddItem(stopBtn, 14, 0, false).
		AddItem(toggleAuth, 18, 0, false).AddItem(rotateBtn, 14, 0, false).
		AddItem(revealBtn, 20, 0, false).AddItem(nil, 0, 1, false)

	upToolbar := tview.NewFlex()
	uStart := toolbarButton("上游启动", func() {
		id := p.selectedUpstream()
		go p.upstreamControl(a, id, "start")
	})
	uStop := toolbarButton("上游停止", func() {
		id := p.selectedUpstream()
		go p.upstreamControl(a, id, "stop")
	})
	uRestart := toolbarButton("上游重启", func() {
		id := p.selectedUpstream()
		go p.upstreamControl(a, id, "restart")
	})
	upToolbar.AddItem(uStart, 14, 0, true).AddItem(uStop, 14, 0, false).
		AddItem(uRestart, 14, 0, false).AddItem(nil, 0, 1, false)

	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(p.gwView, 0, 1, false).
		AddItem(toolbar, 3, 0, false).
		AddItem(sectionTitle("上游进程"), 1, 0, false).
		AddItem(p.upTable, 0, 2, true).
		AddItem(upToolbar, 3, 0, false).
		AddItem(sectionTitle("各 agent 接入端点"), 1, 0, false).
		AddItem(p.epsView, 0, 1, false).
		AddItem(p.tokenView, 0, 1, false)

	return &pageDef{
		prim: bordered(" 聚合网关 ", content),
		focus: p.upTable,
		load: func(a *App) { p.load(a) },
		inst: p,
	}
}

func (p *gatewayPage) load(a *App) {
	status, stErr := a.b.client.Status()
	gw, gwErr := a.b.client.Gateway()
	agents, agErr := a.b.client.Agents(false)
	var token string
	var tokenErr error
	if stErr == nil && status.AuthRequired {
		token, tokenErr = a.b.client.Token()
	}
	a.ui(func() {
		if stErr != nil || gwErr != nil {
			detail := orDash(firstErrText(stErr, gwErr))
			p.gwView.SetText("[red]网关状态获取失败: " + detail + "[-]")
			return
		}
		p.status = status
		p.gw = gw
		if token != "" {
			p.token = token
		}
		p.apply(a, agents, agErr, tokenErr)
	})
}

// firstErrText 两个错误里取非空者（都为空返回空串）。
func firstErrText(errs ...error) string {
	for _, e := range errs {
		if e != nil {
			return e.Error()
		}
	}
	return ""
}

func (p *gatewayPage) apply(a *App, agents []AgentInfo, agErr error, tokenErr error) {
	s := p.status
	gw := p.gw
	var lines []string
	if gw.Running {
		lines = append(lines, fmt.Sprintf("[green]● 网关运行中[-]  端口 [white]%d[-]  聚合端点 [white]http://127.0.0.1:%d/mcp[-]", *gw.Port, *gw.Port))
	} else {
		lines = append(lines, "[gray]○ 网关未运行[-]（启动后各 agent 可经统一端点访问已绑定的 MCP）")
	}
	if s != nil && s.ResyncPending {
		lines = append(lines, "[yellow]⚠ 有配置变更待重新分发（到「Agent 分发」执行同步）[-]")
	}
	p.gwView.SetText(strings.Join(lines, "\n"))

	p.upTable.Clear()
	p.upTable.SetCell(0, 0, headerCell("server"))
	p.upTable.SetCell(0, 1, headerCell("状态"))
	p.upTable.SetCell(0, 2, headerCell("PID"))
	p.upTable.SetCell(0, 3, headerCell("工具数"))
	p.upTable.SetCell(0, 4, headerCell("最近错误"))
	for i, u := range gw.Upstreams {
		row := i + 1
		p.upTable.SetCell(row, 0, tview.NewTableCell(u.ID))
		color := "gray"
		switch u.Status {
		case "ready":
			color = "green"
		case "starting":
			color = "yellow"
		case "error", "backoff":
			color = "red"
		}
		p.upTable.SetCell(row, 1, tview.NewTableCell(fmt.Sprintf("[%s]%s[-]", color, u.Status)))
		pid := "—"
		if u.PID != nil {
			pid = fmt.Sprint(*u.PID)
		}
		p.upTable.SetCell(row, 2, tview.NewTableCell(pid))
		p.upTable.SetCell(row, 3, tview.NewTableCell(fmt.Sprint(u.ToolCount)))
		errText := "—"
		if u.LastError != nil && *u.LastError != "" {
			errText = *u.LastError
		}
		p.upTable.SetCell(row, 4, tview.NewTableCell("[gray]"+errText+"[-]").SetMaxWidth(50))
	}
	if len(gw.Upstreams) == 0 {
		p.upTable.SetCell(1, 0, tview.NewTableCell("[gray]（网关模式下无上游进程；直连 server 由 agent 直接拉起）[-]").SetSelectable(false))
	}

	// 各 agent 端点（鉴权开启时附带 token 的 URL 由 sync 分发，这里展示端点形态）
	var eps []string
	if agErr != nil {
		eps = append(eps, "[red]agent 列表获取失败（端点列表不完整）: "+agErr.Error()+"[-]")
	}
	for _, ag := range agents {
		if !ag.Detected {
			continue
		}
		if gw.Running {
			eps = append(eps, fmt.Sprintf("[cyan]%s[-] http://127.0.0.1:%d/agents/%s/mcp", ag.DisplayName, *gw.Port, ag.AgentType))
		} else {
			eps = append(eps, fmt.Sprintf("[gray]%s （网关未运行）[-]", ag.DisplayName))
		}
	}
	if len(eps) == 0 {
		eps = append(eps, "[gray]（未检测到已安装 agent）[-]")
	}
	p.epsView.SetText(strings.Join(eps, "\n"))
	p.applyToken(a, tokenErr)
}


// applyToken 令牌区渲染（在 ui 闭包/事件循环内调用）。
func (p *gatewayPage) applyToken(a *App, tokenErr error) {
	if p.status == nil {
		return
	}
	if !p.status.AuthRequired {
		p.tokenView.SetText("[gray]令牌鉴权：关闭（环回监听，D7 默认）[-]")
		return
	}
	if tokenErr != nil {
		p.tokenView.SetText("[red]访问令牌读取失败（鉴权已开启）: " + tokenErr.Error() + "[-]")
		return
	}
	token := p.token
	if !p.tokenShown {
		if len(token) > 8 {
			token = token[:4] + "…" + token[len(token)-4:]
		}
	}
	p.tokenView.SetText(fmt.Sprintf("[white]访问令牌[-]（鉴权开启）：[yellow]%s[-]   [gray]（轮换令牌后需重新分发）[-]", token))
}

func (p *gatewayPage) control(a *App, action string) {
	var err error
	if action == "start" {
		_, err = a.b.client.GatewayStart()
	} else {
		_, err = a.b.client.GatewayStop()
	}
	if err != nil {
		a.errd(err)
		return
	}
	a.info(map[string]string{"start": "网关已启动", "stop": "网关已停止"}[action])
	p.load(a)
}

func (p *gatewayPage) selectedUpstream() string {
	row, _ := p.upTable.GetSelection()
	if row <= 0 {
		return ""
	}
	if cell := p.upTable.GetCell(row, 0); cell != nil {
		return cell.Text
	}
	return ""
}

func (p *gatewayPage) upstreamControl(a *App, id, action string) {
	if id == "" {
		a.info("先选中一个上游")
		return
	}
	if err := a.b.client.UpstreamControl(id, action); err != nil {
		a.errd(err)
		return
	}
	a.info("上游已" + map[string]string{"start": "启动", "stop": "停止", "restart": "重启"}[action])
	p.load(a)
}

// toggleAuth 鉴权开关（authOn 已在事件循环内捕获）。
func (p *gatewayPage) toggleAuth(a *App, authOn bool) {
	next := !authOn
	note := "开启后网关端点需要访问令牌，已分发的 agent 配置需重新同步。"
	if !next {
		note = "关闭后网关端点不再校验令牌（仅环回监听）。已分发的 agent 配置需重新同步。"
	}
	a.confirm("切换令牌鉴权", note, func() {
		if err := a.b.client.PatchSettings(map[string]any{"authRequired": next}); err != nil {
			a.errd(err)
			return
		}
		a.info("鉴权已" + enabledWord(next))
		p.load(a)
	})
}

func (p *gatewayPage) rotateToken(a *App) {
	a.confirm("轮换访问令牌", "旧令牌立即失效；如鉴权开启且已分发，需要重新同步。", func() {
		token, resync, err := a.b.client.RotateToken()
		if err != nil {
			a.errd(err)
			return
		}
		a.ui(func() {
			p.token = token
			p.tokenShown = true
			p.revealGen++
			p.applyToken(a, nil)
			// 新令牌临时明文展示 10s 后自动回隐（对齐 Web"查看后回隐"语义）；
			// 期间手动切换/再次轮换会递增代次，本定时器作废
			gen := p.revealGen
			time.AfterFunc(10*time.Second, func() {
				a.post(func() {
					if p.revealGen == gen && p.tokenShown {
						p.tokenShown = false
						p.applyToken(a, nil)
					}
				})
			})
		})
		if resync {
			a.info("令牌已轮换（需要重新分发）")
		} else {
			a.info("令牌已轮换")
		}
		p.load(a)
	})
}
