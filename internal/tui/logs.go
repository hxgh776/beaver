package tui

// 调用日志页：网关调用记录（含失败过滤）与上游 stderr 输出（对齐 Web LogsPage）。

import (
	"fmt"
	"strings"
	"time"

	"github.com/rivo/tview"
)

type logsPage struct {
	calls      []CallRecord
	failOnly   bool
	upstreams  []string
	selUpstream string
	callTable  *tview.Table
	upDrop     *tview.DropDown
	upLogView  *tview.TextView
}

func newLogsPage(a *App) *pageDef {
	p := &logsPage{
		callTable: tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
		upLogView: tview.NewTextView().SetDynamicColors(true).SetWrap(true),
		upDrop:    tview.NewDropDown().SetLabel(" 上游 ").SetOptions([]string{"（无）"}, nil),
	}

	filterCheck := tview.NewCheckbox().SetLabel(" 只看失败 ")
	filterCheck.SetChangedFunc(func(on bool) {
		p.failOnly = on
		p.applyCalls()
	})
	refreshBtn := toolbarButton("刷新", func() { go p.load(a) })

	callsBar := tview.NewFlex().
		AddItem(filterCheck, 16, 0, false).
		AddItem(refreshBtn, 10, 0, false).
		AddItem(nil, 0, 1, false)

	left := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(sectionTitle("调用记录"), 1, 0, false).
		AddItem(callsBar, 1, 0, false).
		AddItem(p.callTable, 0, 1, true)

	upBar := tview.NewFlex().
		AddItem(p.upDrop, 34, 0, false).
		AddItem(nil, 0, 1, false)
	p.upDrop.SetSelectedFunc(func(text string, index int) {
		sel := p.upstreams[index] // 循环内取，安全
		go p.loadUpLog(a, sel)
	})
	right := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(sectionTitle("上游进程输出（stderr）"), 1, 0, false).
		AddItem(upBar, 1, 0, false).
		AddItem(p.upLogView, 0, 1, false)

	content := tview.NewFlex().
		AddItem(left, 0, 3, true).
		AddItem(right, 0, 2, false)

	return &pageDef{
		prim: bordered(" 调用日志（自动刷新 · 与 Web 同为轮询） ", content),
		focus: p.callTable,
		load: func(a *App) { p.load(a) },
	}
}

func (p *logsPage) load(a *App) {
	var prevSel string
	a.ui(func() { prevSel = p.selUpstream }) // 排队读当前选中（字段只在循环内读写）
	calls, callErr := a.b.client.Calls()
	gw, _ := a.b.client.Gateway()
	var logLines []string
	var logErr error
	sel := prevSel
	if gw != nil && len(gw.Upstreams) > 0 {
		if sel == "" {
			sel = gw.Upstreams[0].ID
		}
		logLines, logErr = a.b.client.UpstreamLogs(sel)
	}
	a.ui(func() {
		if callErr != nil {
			p.callTable.Clear()
			p.callTable.SetCell(0, 0, tview.NewTableCell("[red]调用记录获取失败: "+callErr.Error()+"[-]").SetSelectable(false))
		} else {
			p.calls = calls
			p.applyCalls()
		}
		// 上游下拉与输出
		p.upstreams = nil
		if gw != nil {
			for _, u := range gw.Upstreams {
				p.upstreams = append(p.upstreams, u.ID)
			}
		}
		if len(p.upstreams) > 0 {
			p.selUpstream = sel
			p.upDrop.SetOptions(p.upstreams, nil)
			idx := 0
			for i, id := range p.upstreams {
				if id == sel {
					idx = i
					break
				}
			}
			p.upDrop.SetCurrentOption(idx)
		} else {
			p.upDrop.SetOptions([]string{"（无上游进程）"}, nil)
			p.upLogView.SetText("[gray]网关未运行或没有上游进程。[-]")
		}
		if logErr != nil {
			p.upLogView.SetText("[red]上游输出获取失败: " + logErr.Error() + "[-]")
		} else if len(logLines) > 0 {
			p.upLogView.SetText(strings.Join(logLines, "\n"))
		} else if len(p.upstreams) > 0 {
			p.upLogView.SetText("[gray]（暂无输出）[-]")
		}
	})
}

func (p *logsPage) loadUpLog(a *App, sel string) {
	if sel == "" {
		return
	}
	lines, err := a.b.client.UpstreamLogs(sel)
	a.ui(func() {
		if err != nil {
			p.upLogView.SetText("[red]上游输出获取失败: " + err.Error() + "[-]")
			return
		}
		if len(lines) == 0 {
			p.upLogView.SetText("[gray]（暂无输出）[-]")
			return
		}
		p.upLogView.SetText(strings.Join(lines, "\n"))
	})
}

func (p *logsPage) applyCalls() {
	p.callTable.Clear()
	p.callTable.SetCell(0, 0, headerCell("时间"))
	p.callTable.SetCell(0, 1, headerCell("范围"))
	p.callTable.SetCell(0, 2, headerCell("server"))
	p.callTable.SetCell(0, 3, headerCell("工具"))
	p.callTable.SetCell(0, 4, headerCell("结果"))
	p.callTable.SetCell(0, 5, headerCell("耗时"))
	p.callTable.SetCell(0, 6, headerCell("错误"))
	row := 1
	for _, c := range p.calls {
		if p.failOnly && c.Ok {
			continue
		}
		okText, color := "✓", "green"
		errText := "—"
		if !c.Ok {
			okText, color = "✗", "red"
			if c.Error != nil {
				errText = *c.Error
			}
		}
		p.callTable.SetCell(row, 0, tview.NewTableCell("[gray]"+shortTime(c.Ts)+"[-]"))
		p.callTable.SetCell(row, 1, tview.NewTableCell(c.Scope))
		p.callTable.SetCell(row, 2, tview.NewTableCell(c.ServerID))
		p.callTable.SetCell(row, 3, tview.NewTableCell(c.Tool))
		p.callTable.SetCell(row, 4, tview.NewTableCell(fmt.Sprintf("[%s]%s[-]", color, okText)))
		p.callTable.SetCell(row, 5, tview.NewTableCell(fmt.Sprintf("%.0fms", c.DurationMs)))
		p.callTable.SetCell(row, 6, tview.NewTableCell("[gray]"+errText+"[-]").SetMaxWidth(40))
		row++
	}
	if row == 1 {
		p.callTable.SetCell(1, 0, tview.NewTableCell("[gray]（暂无调用记录——网关被 agent 调用后显示）[-]").SetSelectable(false))
	}
}

func shortTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("01-02 15:04:05")
}
