package tui

// Skill 管理页：安装（skills.sh / 仓库集合 / 本地 ZIP）、启停分发、更新与卸载（对齐 Web SkillsPage）。

import (
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rivo/tview"

	"github.com/hxgh776/beaver/internal/skills"
)

type skillsPage struct {
	skills     []skills.WithStatus
	ssotDir    string
	skillTable *tview.Table
	reportView *tview.TextView
	// 工具栏安装入口（测试经 pageStruct 驱动：聚焦后真实按键激活）
	searchBtn *tview.Button
	collBtn   *tview.Button
	zipBtn    *tview.Button
}

func newSkillsPage(a *App) *pageDef {
	p := &skillsPage{
		skillTable: tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
		reportView: tview.NewTextView().SetDynamicColors(true).SetWrap(true),
	}

	toolbar := tview.NewFlex()
	p.searchBtn = toolbarButton("搜索安装（skills.sh）", func() { go p.openSearch(a) })
	p.collBtn = toolbarButton("仓库集合安装", func() { go p.openCollectionInstall(a) })
	p.zipBtn = toolbarButton("本地 ZIP 安装", func() { go p.openZipInstall(a) })
	searchBtn, collBtn, zipBtn := p.searchBtn, p.collBtn, p.zipBtn
	toggleBtn := toolbarButton("分发开关", func() {
		sk := p.selectedSkill()
		go p.openTargets(a, sk)
	})
	updateBtn := toolbarButton("更新", func() {
		sk := p.selectedSkill()
		go p.update(a, sk)
	})
	delBtn := toolbarButton("卸载", func() {
		sk := p.selectedSkill()
		go p.uninstall(a, sk)
	})
	checkBtn := toolbarButton("检查更新", func() { go p.checkUpdates(a) })
	toolbar.AddItem(searchBtn, 24, 0, true).AddItem(collBtn, 18, 0, false).
		AddItem(zipBtn, 18, 0, false).AddItem(toggleBtn, 14, 0, false).
		AddItem(updateBtn, 10, 0, false).AddItem(delBtn, 10, 0, false).
		AddItem(checkBtn, 14, 0, false).AddItem(nil, 0, 1, false)

	p.skillTable.SetSelectedFunc(func(row, _ int) {
		if row >= 1 {
			sk := p.selectedSkill()
			go p.openTargets(a, sk)
		}
	})

	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(toolbar, 3, 0, false).
		AddItem(p.skillTable, 0, 2, true).
		AddItem(sectionTitle("报告"), 1, 0, false).
		AddItem(p.reportView, 0, 1, false)

	return &pageDef{
		prim: bordered(" Skill 管理（回车配置分发开关） ", content),
		focus: p.skillTable,
		load: func(a *App) { p.load(a) },
		inst: p,
	}
}

func (p *skillsPage) selectedSkill() *skills.WithStatus {
	row, _ := p.skillTable.GetSelection()
	if row <= 0 || row-1 >= len(p.skills) {
		return nil
	}
	return &p.skills[row-1]
}

func (p *skillsPage) load(a *App) {
	list, ssotDir, err := a.b.client.Skills()
	a.ui(func() {
		if err != nil {
			p.skillTable.Clear()
			p.skillTable.SetCell(0, 0, tview.NewTableCell("[red]加载失败: "+err.Error()+"[-]").SetSelectable(false))
			return
		}
		p.skills = list
		p.ssotDir = ssotDir
		p.apply()
	})
}

func (p *skillsPage) apply() {
	p.skillTable.Clear()
	p.skillTable.SetCell(0, 0, headerCell("技能"))
	p.skillTable.SetCell(0, 1, headerCell("来源"))
	p.skillTable.SetCell(0, 2, headerCell("分发（已投影/启用）"))
	p.skillTable.SetCell(0, 3, headerCell("安装时间"))
	for i, sk := range p.skills {
		row := i + 1
		p.skillTable.SetCell(row, 0, tview.NewTableCell(sk.Name))
		p.skillTable.SetCell(row, 1, tview.NewTableCell("[gray]"+sk.Source+"[-]"))
		enabled, projected := 0, 0
		var marks []string
		for _, t := range sk.Targets {
			if t.Enabled {
				enabled++
			}
			if t.Projected {
				projected++
			}
			flag := "○"
			color := "gray"
			if t.Enabled && t.Projected {
				flag, color = "●", "green"
			} else if t.Enabled {
				flag, color = "◐", "yellow"
			}
			if t.Modified {
				color = "red"
			}
			marks = append(marks, fmt.Sprintf("[%s]%s%s[-]", color, flag, t.AgentType))
		}
		sort.Strings(marks)
		p.skillTable.SetCell(row, 2, tview.NewTableCell(fmt.Sprintf("%d/%d 启用 · %s", enabled, len(sk.Targets), strings.Join(marks, " "))).SetMaxWidth(60))
		p.skillTable.SetCell(row, 3, tview.NewTableCell("[gray]"+shortDate(sk.InstalledAt)+"[-]"))
	}
	if len(p.skills) == 0 {
		p.skillTable.SetCell(1, 0, tview.NewTableCell("[gray]（未安装技能——可从 skills.sh 搜索、整仓集合或本地 ZIP 安装）[-]").SetSelectable(false))
	}
}

func shortDate(iso string) string {
	if len(iso) >= 10 {
		return iso[:10]
	}
	return iso
}

func (p *skillsPage) report(a *App, text string) {
	a.ui(func() { p.reportView.SetText(text) })
}

// openSearch skills.sh 搜索 + 安装（安装前选 enableFor）。
// 部件操作经 ui 排队；可在任意 goroutine 调用。
func (p *skillsPage) openSearch(a *App) {
	a.ui(func() {
		resultTable := tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
		var searchState *SkillSearchResult
		form := tview.NewForm()
		form.AddInputField("关键词", "", 26, nil, nil) // item 0
		form.AddButton("搜索", func() {
			query := form.GetFormItem(0).(*tview.InputField).GetText() // 循环内读
			go func() {
				res, err := a.b.client.SkillSearch(query, 12, 0)
				a.ui(func() {
					if err != nil {
						p.reportView.SetText("[red]skills.sh 检索失败: " + err.Error() + "[-]")
						return
					}
					searchState = res
					resultTable.Clear()
					resultTable.SetCell(0, 0, headerCell("技能"))
					resultTable.SetCell(0, 1, headerCell("安装量"))
					resultTable.SetCell(0, 2, headerCell("仓库"))
					for i, s := range res.Skills {
						row := i + 1
						resultTable.SetCell(row, 0, tview.NewTableCell(s.Name))
						resultTable.SetCell(row, 1, tview.NewTableCell(fmt.Sprintf("%.0f", s.Installs)))
						resultTable.SetCell(row, 2, tview.NewTableCell("[gray]"+s.Repo+"@"+s.Branch+"[-]").SetMaxWidth(40))
					}
					// 结果就绪后焦点交给结果表（方向键选行，回车 = 安装所选）
					a.tapp.SetFocus(resultTable)
				})
			}()
		})
		installSelected := func() {
			row, _ := resultTable.GetSelection() // 循环内读
			if searchState == nil || row <= 0 || row-1 >= len(searchState.Skills) {
				a.info("先搜索并选中技能")
				return
			}
			s := searchState.Skills[row-1]
			// pickEnableFor 的 showModal 原子替换当前模态，无需先显式关闭（避免异步乱序）
			p.pickEnableFor(a, func(enableFor []string) {
				go func() {
					skill, err := a.b.client.SkillInstall(s.Repo, s.SkillID, s.Branch, enableFor)
					if err != nil {
						a.errd(err)
						return
					}
					a.info("已安装 " + skill.Name)
					p.load(a)
				}()
			})
		}
		form.AddButton("安装所选", installSelected)
		// 结果表回车 = 安装所选（键盘直达，不必 Tab 摸回按钮）
		resultTable.SetSelectedFunc(func(row, _ int) { installSelected() })
		form.AddButton("关闭", func() { a.closeModal() })

		body := tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(form, 3, 0, true).
			AddItem(resultTable, 0, 1, false)
		a.showModal(centered(bordered(" skills.sh 搜索 ", body), 90, 24))
	})
}

// pickEnableFor 选择分发目标（8 个 agent 复选框）后回调。post 异步调度：
// 调用方可能在事件循环内（搜索/集合安装按钮），也可能在 goroutine 内（ZIP 安装）。
func (p *skillsPage) pickEnableFor(a *App, onDone func([]string)) {
	a.post(func() {
		// 只列本地目录投影目标：远端型（multica workspace 库）在安装期不勾选，
		// 避免 CLI 未就绪/未登录阻塞本地安装；安装后可在分发开关里单独启用。
		targets := make([]string, 0, len(skills.SkillAgentList))
		for _, at := range skills.SkillAgentList {
			if _, local := skills.SkillAgentDirs[at]; local {
				targets = append(targets, at)
			}
		}
		form := tview.NewForm()
		for _, at := range targets {
			form.AddCheckbox(at, false, nil)
		}
		form.AddButton("安装", func() {
			var enableFor []string
			for i, at := range targets {
				if form.GetFormItem(i).(*tview.Checkbox).IsChecked() {
					enableFor = append(enableFor, at)
				}
			}
			a.closeModal()
			// go 化：onDone 可能做阻塞 client 调用（ZIP 安装路径），不得占住事件循环
			go onDone(enableFor)
		})
		form.AddButton("取消", func() { a.closeModal() })
		form.SetBorder(true).SetTitle(" 选择分发目标 ").SetTitleAlign(tview.AlignLeft)
		a.showModal(centered(form, 60, len(targets)+5))
	})
}

// openCollectionInstall 仓库集合安装；部件操作经 ui 排队。
func (p *skillsPage) openCollectionInstall(a *App) {
	a.ui(func() {
		form := tview.NewForm()
		form.AddInputField("仓库坐标（owner/repo）", "", 30, nil, nil) // item 0
		form.AddInputField("分支（可空）", "", 16, nil, nil)            // item 1
		form.AddButton("安装集合", func() {
			repo := form.GetFormItem(0).(*tview.InputField).GetText()  // 循环内读
			branch := form.GetFormItem(1).(*tview.InputField).GetText() // 循环内读
			if len(strings.TrimSpace(repo)) < 3 {
				a.info("仓库坐标不能为空")
				return
			}
			// showModal 原子替换当前模态，无需先显式关闭
			p.pickEnableFor(a, func(enableFor []string) {
				go func() {
					report, err := a.b.client.SkillInstallCollection(repo, branch, enableFor)
					if err != nil {
						a.errd(err)
						return
					}
					msg := fmt.Sprintf("集合安装完成：成功 %d 个", len(report.Installed))
					for _, f := range report.Failed {
						msg += fmt.Sprintf("\n[yellow]跳过 %s: %s[-]", f.Directory, f.Reason)
					}
					a.ui(func() { p.reportView.SetText(msg) })
					a.info(msg)
					p.load(a)
				}()
			})
		})
		form.AddButton("取消", func() { a.closeModal() })
		form.SetBorder(true).SetTitle(" 仓库集合安装 ").SetTitleAlign(tview.AlignLeft)
		a.showModal(centered(form, 70, 9))
	})
}

// openZipInstall 本地 ZIP 安装；部件操作经 ui 排队。
func (p *skillsPage) openZipInstall(a *App) {
	a.ui(func() {
		form := tview.NewForm()
		form.AddInputField("ZIP 路径", "", 44, nil, nil) // item 0
		form.AddButton("安装（单技能）", func() {
			zipPath := form.GetFormItem(0).(*tview.InputField).GetText()
			go p.installZip(a, zipPath, false)
		})
		form.AddButton("安装（集合）", func() {
			zipPath := form.GetFormItem(0).(*tview.InputField).GetText()
			go p.installZip(a, zipPath, true)
		})
		form.AddButton("取消", func() { a.closeModal() })
		form.SetBorder(true).SetTitle(" 本地 ZIP 安装 ").SetTitleAlign(tview.AlignLeft)
		a.showModal(centered(form, 76, 9))
	})
}

// installZip 读本地 ZIP → base64 → 安装（异步 goroutine 调用）。
func (p *skillsPage) installZip(a *App, zipPath string, collection bool) {
	if zipPath == "" {
		a.info("先填入 ZIP 路径")
		return
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		a.errd(err)
		return
	}
	p.pickEnableFor(a, func(enableFor []string) {
		skill, report, err := a.b.client.SkillInstallZip(base64.StdEncoding.EncodeToString(data), collection, enableFor)
		if err != nil {
			a.errd(err)
			return
		}
		if collection && report != nil {
			msg := fmt.Sprintf("ZIP 集合安装完成：成功 %d 个", len(report.Installed))
			for _, f := range report.Failed {
				msg += fmt.Sprintf("\n[yellow]跳过 %s: %s[-]", f.Directory, f.Reason)
			}
			a.ui(func() { p.reportView.SetText(msg) })
		} else if skill != nil {
			a.info("已安装 " + skill.Name)
		}
		p.load(a)
	})
}

// openTargets 分发开关（sk 已在事件循环内捕获）；部件操作经 ui 排队。
func (p *skillsPage) openTargets(a *App, sk *skills.WithStatus) {
	if sk == nil {
		a.info("先选中一个技能")
		return
	}
	a.ui(func() {
		form := tview.NewForm()
		form.AddTextView("", fmt.Sprintf("%s（%s）", sk.Name, sk.Source), 40, 1, true, false)
		for _, t := range sk.Targets {
			t := t
			// 变更即写入（与 Web chip 点击等价）；client 调用异步
			form.AddCheckbox(t.AgentType, t.Enabled, func(on bool) {
				go func() {
					_, err := a.b.client.SkillToggle(sk.ID, t.AgentType, on)
					if err != nil {
						a.errd(err)
						return
					}
					a.info(fmt.Sprintf("%s @ %s 已%s", sk.ID, t.AgentType, enabledWord(on)))
				}()
			})
		}
		form.AddButton("完成", func() {
			a.closeModal()
			go p.load(a) // load 含 client 调用，不占事件循环
		})
		form.SetBorder(true).SetTitle(" 分发开关 — " + sk.ID + " ").SetTitleAlign(tview.AlignLeft)
		a.showModal(centered(form, 64, len(sk.Targets)+6))
	})
}

// update 更新技能（sk 已在事件循环内捕获）。
func (p *skillsPage) update(a *App, sk *skills.WithStatus) {
	if sk == nil {
		a.info("先选中一个技能")
		return
	}
	go func() {
		skill, err := a.b.client.SkillUpdate(sk.ID)
		if err != nil {
			a.errd(err)
			return
		}
		a.info("已更新 " + skill.Name)
		p.load(a)
	}()
}

// uninstall 卸载技能（sk 已在事件循环内捕获）。
func (p *skillsPage) uninstall(a *App, sk *skills.WithStatus) {
	if sk == nil {
		a.info("先选中一个技能")
		return
	}
	a.confirm("卸载技能", fmt.Sprintf("卸载「%s」？用户改动过的目录会被保留，卸载前自动备份。", sk.Name), func() {
		res, err := a.b.client.SkillUninstall(sk.ID)
		if err != nil {
			a.errd(err)
			return
		}
		msg := "已卸载"
		if len(res.Preserved) > 0 {
			msg += "（保留用户改动: " + strings.Join(res.Preserved, ", ") + "）"
		}
		if len(res.Warnings) > 0 {
			msg += "；远端警告: " + strings.Join(res.Warnings, "；")
		}
		a.info(msg)
		p.load(a)
	})
}

func (p *skillsPage) checkUpdates(a *App) {
	go func() {
		report, err := a.b.client.SkillUpdates()
		a.ui(func() {
			if err != nil {
				a.errd(err)
				return
			}
			var lines []string
			if len(report.Updates) == 0 {
				lines = append(lines, "[green]全部技能均为最新。[-]")
			} else {
				lines = append(lines, fmt.Sprintf("[yellow]发现 %d 个可更新技能：[-]", len(report.Updates)))
				for _, u := range report.Updates {
					if u.Missing {
						lines = append(lines, fmt.Sprintf("  [red]%s 本地缺失[-]", u.SkillID))
						continue
					}
					lines = append(lines, fmt.Sprintf("  %s（%s）本地 %s → 远端 %s", u.SkillID, u.Repo, shortHash(u.LocalHash), shortHash(u.RemoteHash)))
				}
				lines = append(lines, "[gray]选中技能后点「更新」逐个升级。[-]")
			}
			for _, f := range report.Failures {
				lines = append(lines, fmt.Sprintf("[red]检查失败 %s: %s[-]", f.Repo, f.Reason))
			}
			p.reportView.SetText(strings.Join(lines, "\n"))
		})
	}()
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
