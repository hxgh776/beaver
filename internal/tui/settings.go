package tui

// 设置页：端口、日志级别、开机自启、备份恢复、数据目录与关于（对齐 Web SettingsPage）。

import (
	"fmt"
	"strings"

	"github.com/rivo/tview"

	"github.com/hxgh776/beaver/internal/version"
)

type settingsPage struct {
	status      *StatusInfo
	settings    *SettingsInfo
	autostart   *AutostartInfo
	backups     *BackupGroups
	infoView    *tview.TextView
	backupTable *tview.Table
	restoreRows []restoreRow
	portField   *tview.InputField
	levelDrop   *tview.DropDown
	form        *tview.Form
}

var logLevels = []string{"debug", "info", "warn", "error"}

func newSettingsPage(a *App) *pageDef {
	p := &settingsPage{
		infoView:    tview.NewTextView().SetDynamicColors(true).SetWrap(true),
		backupTable: tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
	}

	form := tview.NewForm()
	// 标签保持短（左列宽度有限，长标签会把输入框挤出屏幕）；重启提示放信息区
	p.portField = tview.NewInputField().SetLabel("API 端口").SetFieldWidth(10)
	form.AddFormItem(p.portField) // item 0
	p.levelDrop = tview.NewDropDown().SetLabel("日志级别").SetOptions(logLevels, nil).SetCurrentOption(1)
	form.AddFormItem(p.levelDrop) // item 1
	form.AddButton("保存运行参数", func() { p.saveSettings(a, form) })
	p.form = form // 测试经 pageStruct 取用（GetButton 驱动保存）
	form.AddButton("开机自启：开", func() { go p.setAutostart(a, true) })
	form.AddButton("开机自启：关", func() { go p.setAutostart(a, false) })
	form.AddButton("打开数据目录", func() { go p.openDir(a) })
	form.SetBorder(true).SetTitle(" 运行参数 ").SetTitleAlign(tview.AlignLeft)

	p.backupTable.SetSelectedFunc(func(row, _ int) {
		var target *restoreRow
		if row >= 1 && row-1 < len(p.restoreRows) { // 循环内取行载荷
			r := p.restoreRows[row-1]
			target = &r
		}
		go p.restoreSelected(a, target)
	})

	left := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(form, 10, 0, true). // 2 个表单项（各占 2 行含间距）+ 按钮行 + 边框
		AddItem(sectionTitle("关于"), 1, 0, false).
		AddItem(p.infoView, 0, 1, false)
	right := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(sectionTitle("备份（回车恢复选中项 · 覆盖前自动再备份）"), 1, 0, false).
		AddItem(p.backupTable, 0, 1, false)

	content := tview.NewFlex().
		AddItem(left, 0, 1, true).
		AddItem(right, 0, 1, false)

	return &pageDef{
		prim: bordered(" 设置 ", content),
		focus: p.backupTable,
		load: func(a *App) { p.load(a) },
		inst: p,
	}
}

// restoreRows 备份表格行的操作载荷（agent 备份 vs config 备份）。
type restoreRow struct {
	agentType string // "" = config 备份
	name      string
}

func (p *settingsPage) load(a *App) {
	status, stErr := a.b.client.Status()
	settings, seErr := a.b.client.Settings()
	autostart, auErr := a.b.client.Autostart()
	backups, baErr := a.b.client.Backups()
	a.ui(func() {
		if stErr != nil || seErr != nil {
			p.infoView.SetText("[red]设置读取失败[-]")
			return
		}
		p.status = status
		p.settings = settings
		p.autostart = autostart
		// 表单回填当前值（否则下拉停在默认 info：当前为 debug/warn/error 时
		// 只改端口保存就会把日志级别误写成 info）
		p.portField.SetText(fmt.Sprint(settings.Port))
		for idx, lv := range logLevels {
			if lv == settings.LogLevel {
				p.levelDrop.SetCurrentOption(idx)
				break
			}
		}
		p.applyInfo(auErr, autostart)
		if baErr != nil {
			p.backupTable.Clear()
			p.backupTable.SetCell(0, 0, tview.NewTableCell("[red]备份读取失败: "+baErr.Error()+"[-]").SetSelectable(false))
			return
		}
		p.backups = backups
		p.applyBackups()
	})
}

func (p *settingsPage) applyInfo(auErr error, autostart *AutostartInfo) {
	lines := []string{
		fmt.Sprintf("版本 [white]beaver %s（daemon %s）[-]", version.String(), orDash(p.status.Version)),
		fmt.Sprintf("数据目录 [white]%s[-]", p.status.HomeDir),
		fmt.Sprintf("端口 [white]%d[-]   日志级别 [white]%s[-]   令牌鉴权 [white]%v[-]", p.settings.Port, p.settings.LogLevel, p.settings.AuthRequired),
		"[gray]（端口保存后需重启 daemon：beaver stop && beaver start）[-]",
	}
	if auErr != nil {
		lines = append(lines, "[red]自启状态读取失败: "+auErr.Error()+"[-]")
	} else if autostart.Installed {
		lines = append(lines, "[green]开机自启：已安装[-]")
	} else {
		lines = append(lines, "[gray]开机自启：未安装[-]")
	}
	p.infoView.SetText(strings.Join(lines, "\n"))
}

func (p *settingsPage) applyBackups() {
	rows := []restoreRow{}
	p.backupTable.Clear()
	p.backupTable.SetCell(0, 0, headerCell("类别"))
	p.backupTable.SetCell(0, 1, headerCell("文件"))
	p.backupTable.SetCell(0, 2, headerCell("大小"))
	p.backupTable.SetCell(0, 3, headerCell("修改时间"))
	row := 1
	for _, g := range p.backups.Groups {
		for _, f := range g.Files {
			p.backupTable.SetCell(row, 0, tview.NewTableCell(g.AgentType))
			p.backupTable.SetCell(row, 1, tview.NewTableCell(f.Name))
			p.backupTable.SetCell(row, 2, tview.NewTableCell(fmt.Sprintf("%dB", f.Size)))
			p.backupTable.SetCell(row, 3, tview.NewTableCell("[gray]"+shortTime(f.ModifiedAt)+"[-]"))
			row++
			rows = append(rows, restoreRow{agentType: g.AgentType, name: f.Name})
		}
	}
	for _, f := range p.backups.ConfigBackups {
		p.backupTable.SetCell(row, 0, tview.NewTableCell("[cyan]config[-]"))
		p.backupTable.SetCell(row, 1, tview.NewTableCell(f.Name))
		p.backupTable.SetCell(row, 2, tview.NewTableCell(fmt.Sprintf("%dB", f.Size)))
		p.backupTable.SetCell(row, 3, tview.NewTableCell("[gray]"+shortTime(f.ModifiedAt)+"[-]"))
		row++
		rows = append(rows, restoreRow{agentType: "", name: f.Name})
	}
	p.restoreRows = rows
	if row == 1 {
		p.backupTable.SetCell(1, 0, tview.NewTableCell("[gray]（暂无备份）[-]").SetSelectable(false))
	}
}

// restoreSelected 恢复备份（r 已在事件循环内捕获）。
func (p *settingsPage) restoreSelected(a *App, r *restoreRow) {
	if r == nil {
		return
	}
	what := fmt.Sprintf("%s 的备份 %s", r.agentType, r.name)
	if r.agentType == "" {
		what = fmt.Sprintf("管理配置备份 %s", r.name)
	}
	a.confirm("恢复备份", fmt.Sprintf("用 %s 覆盖当前配置？当前文件会先自动再备份。", what), func() {
		body := map[string]any{"name": r.name}
		if r.agentType == "" {
			body["config"] = true
		} else {
			body["agentType"] = r.agentType
		}
		if err := a.b.client.RestoreBackup(body); err != nil {
			a.errd(err)
			return
		}
		a.info("已恢复")
		p.load(a)
	})
}

// saveSettings 保存按钮回调（事件循环内读表单值，随后异步提交）。
func (p *settingsPage) saveSettings(a *App, form *tview.Form) {
	if p.settings == nil {
		return
	}
	portField := form.GetFormItem(0).(*tview.InputField)
	levelDrop := form.GetFormItem(1).(*tview.DropDown)
	curPort := p.settings.Port // 循环内读页面数据
	curLevel := p.settings.LogLevel

	portText := strings.TrimSpace(portField.GetText())
	_, level := levelDrop.GetCurrentOption()
	go func() {
		fields := map[string]any{}
		if portText != "" && portText != fmt.Sprint(curPort) {
			var port int
			if _, err := fmt.Sscanf(portText, "%d", &port); err != nil || port < 1 || port > 65535 {
				a.errd(fmt.Errorf("端口非法: %s", portText))
				return
			}
			fields["port"] = port
		}
		if level != curLevel {
			fields["logLevel"] = level
		}
		if len(fields) == 0 {
			a.info("没有要保存的修改")
			return
		}
		if err := a.b.client.PatchSettings(fields); err != nil {
			a.errd(err)
			return
		}
		a.info("设置已保存")
		if _, ok := fields["port"]; ok {
			a.info("端口已更新——需重启 daemon（beaver stop && beaver start）生效")
		}
		p.load(a)
	}()
}

func (p *settingsPage) setAutostart(a *App, installed bool) {
	info, err := a.b.client.SetAutostart(installed)
	if err != nil {
		a.errd(err)
		return
	}
	a.ui(func() { p.autostart = info })
	if installed {
		a.info("开机自启已安装（登录时静默拉起 daemon）")
	} else {
		a.info("开机自启已移除")
	}
	p.load(a)
}

func (p *settingsPage) openDir(a *App) {
	if err := a.b.client.OpenDataDir(); err != nil {
		a.errd(err)
		return
	}
	a.info("已在系统文件管理器打开数据目录")
}
