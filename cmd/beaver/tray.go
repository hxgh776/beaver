package main

// beaver tray：托盘常驻壳（docs/desktop-plan.md §4 M2 的 Go 原生落地）。
// 菜单五项：打开面板 / 网关启停 / 立即同步 / 开机自启开关 / 退出。
// 双模式：daemon 未跑 = 自持有（in-process 起 daemon，退出全停）；已跑 = 远端（动作走 HTTP API）。
// 退出语义 = 停 daemon + 撤托盘；托盘退出 ≠ 关窗口（浏览器/面板窗口独立存活）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/getlantern/systray"

	"github.com/hxgh776/beaver/internal/autostart"
	"github.com/hxgh776/beaver/internal/daemon"
	"github.com/hxgh776/beaver/internal/desktop"
	"github.com/hxgh776/beaver/internal/gateway"
	"github.com/hxgh776/beaver/internal/store"
)

func cmdTray() error {
	// 远端模式判定：daemon 已在跑则不重复起，动作走 API
	s := store.NewStore(homeOverride)
	config, err := s.Load()
	if err != nil {
		return err
	}
	pid, _ := daemon.ReadPid(s.PidPath())
	remote := pid > 0 && daemon.IsPidAlive(pid)

	var d *daemon.Daemon
	var gw *gateway.Gateway
	if !remote {
		d = daemon.New(daemon.Options{HomeDir: homeOverride})
		gw = gateway.New(d)
		gw.Attach()
		if err := d.Start(); err != nil {
			return err
		}
		if err := os.WriteFile(daemon.PidFilePath(d.Store.HomeDir), []byte(fmt.Sprint(os.Getpid())), 0o644); err != nil {
			return err
		}
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", config.Settings.Port)
	tray := &trayApp{
		homeOverride: homeOverride,
		base:         base,
		token:        config.Settings.Token,
		d:            d,
		gw:           gw,
		ownsDaemon:   !remote,
	}
	systray.Run(tray.onReady, func() {
		if d != nil {
			_ = gw.Detach()
			_ = d.Stop()
			_ = os.Remove(daemon.PidFilePath(d.Store.HomeDir))
		}
	})
	return nil
}

type trayApp struct {
	homeOverride string
	base         string
	token        string
	d            *daemon.Daemon
	gw           *gateway.Gateway
	ownsDaemon   bool

	mGateway *systray.MenuItem
	mAuto    *systray.MenuItem
}

func (t *trayApp) onReady() {
	systray.SetIcon(desktop.TrayIconICO())
	systray.SetTooltip("Beaver（河狸）——本地 Agent 能力总控台\n" + t.base)

	mOpen := systray.AddMenuItem("打开面板", "在独立应用窗口打开 Web UI")
	t.mGateway = systray.AddMenuItemCheckbox("网关", "MCP 网关启停", t.gatewayRunning())
	mSync := systray.AddMenuItem("立即同步", "把注册表分发到各 agent")
	t.mAuto = systray.AddMenuItemCheckbox("开机自启", "登录时静默拉起 daemon", autostart.IsInstalled())
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", t.quitLabel())

	go t.refreshLoop()

	for {
		select {
		case <-mOpen.ClickedCh:
			_ = desktop.OpenAppWindow(t.base)
		case <-t.mGateway.ClickedCh:
			t.toggleGateway()
		case <-mSync.ClickedCh:
			t.syncNow()
		case <-t.mAuto.ClickedCh:
			t.toggleAutostart()
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func (t *trayApp) quitLabel() string {
	if t.ownsDaemon {
		return "退出（停 daemon 并撤托盘）"
	}
	return "退出托盘（daemon 保留后台运行）"
}

func (t *trayApp) gatewayRunning() bool {
	if t.gw != nil {
		return t.gw.Status().Running
	}
	_, data, err := apiClient("GET", "/api/gateway", nil, t.base, t.token)
	if err != nil {
		return false
	}
	running, _ := data["running"].(bool)
	return running
}

func (t *trayApp) toggleGateway() {
	if t.gw != nil {
		if t.gw.Status().Running {
			_ = t.gw.Detach()
		} else {
			t.gw.Attach()
		}
		t.mGateway.Check()
		if !t.gatewayRunning() {
			t.mGateway.Uncheck()
		}
		return
	}
	action := "start"
	if t.gatewayRunning() {
		action = "stop"
	}
	_, _, _ = apiClient("POST", "/api/gateway/"+action, nil, t.base, t.token)
	if t.gatewayRunning() {
		t.mGateway.Check()
	} else {
		t.mGateway.Uncheck()
	}
}

func (t *trayApp) syncNow() {
	if t.d != nil {
		if _, err := t.d.Sync(false, nil); err == nil {
			_ = t.d.SaveConfig()
		}
		return
	}
	_, _, _ = apiClient("POST", "/api/sync", map[string]any{}, t.base, t.token)
}

func (t *trayApp) toggleAutostart() {
	if autostart.IsInstalled() {
		_ = autostart.Uninstall()
		t.mAuto.Uncheck()
		return
	}
	if _, err := autostart.Install(t.homeOverride); err == nil {
		t.mAuto.Check()
	}
}

// refreshLoop 周期回读网关/自启状态，纠正外部变更造成的勾选漂移。
func (t *trayApp) refreshLoop() {
	for {
		time.Sleep(3 * time.Second)
		if t.gatewayRunning() {
			t.mGateway.Check()
		} else {
			t.mGateway.Uncheck()
		}
	}
}

// apiClient 与 CLI 的 api() 同款（托盘远端模式复用 HTTP 通道）。
func apiClient(method, pathname string, body any, base, token string) (int, map[string]any, error) {
	var rdr io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, base+pathname, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	client := &http.Client{Timeout: 300 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	var data map[string]any
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&data)
	return res.StatusCode, data, nil
}
