package tui

// MCP 发现页：九源 registry 检索与一键收录（对齐 Web DiscoveryPage）。

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rivo/tview"
)

var searchSources = []string{"official", "github", "smithery", "npm", "modelscope", "baidu", "cline", "docker", "mcpm"}

type discoveryPage struct {
	query    string
	source   string
	offset   int
	hasMore  bool
	notes    string
	results  *RegistrySearchResult
	known    map[string]bool // 已收录（按建议名匹配，粗略标记）
	list     *tview.Table
	infoView *tview.TextView
	searchBox *tview.InputField
	sourceDrop *tview.DropDown
}

func newDiscoveryPage(a *App) *pageDef {
	p := &discoveryPage{
		list:     tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
		infoView: tview.NewTextView().SetDynamicColors(true).SetWrap(true),
	}

	p.searchBox = tview.NewInputField().SetLabel(" 查询 ").SetFieldWidth(30)
	p.sourceDrop = tview.NewDropDown().SetLabel(" 源 ").SetOptions(toStrings(searchSources), nil).SetCurrentOption(0)

	searchBtn := toolbarButton("搜索", func() {
		query, source := p.searchInputs() // 循环内读部件
		go p.search(a, query, source, 0, true)
	})
	nextBtn := toolbarButton("下一页", func() {
		query, source := p.searchInputs()
		offset := p.offset
		go p.search(a, query, source, offset, false)
	})
	bar := tview.NewFlex().
		AddItem(p.searchBox, 34, 0, true).
		AddItem(p.sourceDrop, 26, 0, false).
		AddItem(searchBtn, 10, 0, false).
		AddItem(nextBtn, 10, 0, false).
		AddItem(nil, 0, 1, false)

	p.list.SetSelectedFunc(func(row, _ int) {
		r := p.selectedSuggestion(row) // 循环内取选中项
		go p.addSelected(a, r)
	})

	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(bar, 1, 0, false).
		AddItem(p.infoView, 2, 0, false).
		AddItem(p.list, 0, 1, true)

	return &pageDef{
		prim: bordered(" MCP 发现（回车收录选中项） ", content),
		focus: p.searchBox,
		load: func(a *App) { p.load(a) },
	}
}

func toStrings(list []string) []string { return list }

func (p *discoveryPage) selectedSuggestion(row int) *RegistrySuggestion {
	if p.results == nil || row < 1 || row-1 >= len(p.results.Servers) {
		return nil
	}
	return &p.results.Servers[row-1]
}

func (p *discoveryPage) load(a *App) {
	servers, err := a.b.client.Servers()
	a.ui(func() {
		p.known = map[string]bool{}
		if err == nil {
			for _, s := range servers {
				p.known[s.Name] = true
				p.known[s.ID] = true
			}
		}
		p.apply()
	})
	if p.results == nil {
		p.search(a, "", "official", 0, true) // 首次进入自动拉官方源
	}
}

// searchInputs 读取搜索框与源下拉（事件循环内调用）。
func (p *discoveryPage) searchInputs() (string, string) {
	_, source := p.sourceDrop.GetCurrentOption()
	return p.searchBox.GetText(), source
}

// search 九源检索（异步 goroutine 调用；reset=true 覆盖结果并从第 0 页开始）。
func (p *discoveryPage) search(a *App, query, source string, offset int, reset bool) {
	if reset {
		offset = 0
	}
	res, err := a.b.client.RegistrySearch(query, source, 12, offset)
	a.ui(func() {
		if err != nil {
			p.infoView.SetText("[red]检索失败: " + err.Error() + "[-]")
			return
		}
		if reset {
			p.results = res
		} else if p.results != nil {
			p.results.Servers = append(p.results.Servers, res.Servers...)
			p.results.HasMore = res.HasMore
			if res.Notes != nil {
				p.results.Notes = res.Notes
			}
		}
		p.offset = offset + 12
		p.hasMore = res.HasMore
		p.query = query
		p.source = source
		var notes []string
		if len(res.Notes) > 0 {
			notes = append(notes, "[gray]提示: "+strings.Join(res.Notes, "；")+"[-]")
		}
		notes = append(notes, fmt.Sprintf("[gray]来源 %s · 共 %d 条 · 回车收录，百度源条目仅浏览[-]", orDash(res.Source), len(res.Servers)))
		p.infoView.SetText(strings.Join(notes, "\n"))
		p.apply()
	})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (p *discoveryPage) apply() {
	p.list.Clear()
	p.list.SetCell(0, 0, headerCell("名称"))
	p.list.SetCell(0, 1, headerCell("热度"))
	p.list.SetCell(0, 2, headerCell("状态"))
	p.list.SetCell(0, 3, headerCell("简介"))
	if p.results == nil {
		return
	}
	for i, r := range p.results.Servers {
		row := i + 1
		p.list.SetCell(row, 0, tview.NewTableCell(r.Name))
		hot := ""
		if r.UseCount != nil {
			hot = fmt.Sprintf("%.0f", *r.UseCount)
		}
		p.list.SetCell(row, 1, tview.NewTableCell("[gray]"+hot+"[-]"))
		status := "[gray]仅浏览[-]"
		if r.Suggestion != nil {
			if p.known[r.Name] {
				status = "[green]✓ 已添加[-]"
			} else {
				status = "[yellow]可添加[-]"
			}
		}
		p.list.SetCell(row, 2, tview.NewTableCell(status))
		desc := r.Description
		if len(desc) > 80 {
			desc = desc[:80] + "…"
		}
		p.list.SetCell(row, 3, tview.NewTableCell("[gray]"+desc+"[-]").SetMaxWidth(56))
	}
	if len(p.results.Servers) == 0 {
		p.list.SetCell(1, 0, tview.NewTableCell("[gray]（无结果）[-]").SetSelectable(false))
	}
}

// addSelected 收录确认（r 已在事件循环内捕获）。
func (p *discoveryPage) addSelected(a *App, r *RegistrySuggestion) {
	if r == nil {
		return
	}
	if r.Suggestion == nil {
		a.info("该条目无可执行建议（仅浏览）")
		return
	}
	body := suggestionBody(r.Suggestion)
	name := r.Name
	a.confirm("收录 MCP server", fmt.Sprintf("添加「%s」（来源 %s）？添加后到「Agent 分发」写回各 agent。", name, orDash(r.Source)), func() {
		if _, err := a.b.client.CreateServer(body); err != nil {
			a.errd(err)
			return
		}
		a.info("已收录 " + name)
		p.load(a)
	})
}

// suggestionBody Suggestion 内嵌的 UpsertServerInput 直接作为 POST /api/servers 请求体
//（Web 端同款：r.suggestion 原样提交）。
func suggestionBody(in any) map[string]any {
	data, _ := json.Marshal(in)
	body := map[string]any{}
	_ = json.Unmarshal(data, &body)
	return body
}
