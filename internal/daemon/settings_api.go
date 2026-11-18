package daemon

// 设置页配套后端（docs/desktop-plan.md §5 M3）：
// autostart 开关 / 备份列表与恢复（恢复清 syncState 哈希）/ 打开数据目录 / hasOnboarded。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/hxgh776/beaver/internal/autostart"
	"github.com/hxgh776/beaver/internal/backup"
	"github.com/hxgh776/beaver/internal/fsutil"
	"github.com/hxgh776/beaver/internal/store"
)

func registerSettingsRoutes(rt *Router, d *Daemon) {
	// —— 开机自启（daemon 进程内执行 autostart 逻辑） ——
	rt.Get("/api/autostart", func(ctx *RequestContext) {
		SendJSON(ctx.Res, 200, map[string]any{"installed": autostart.IsInstalled()})
	})

	rt.Put("/api/autostart", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		installed, _ := body["installed"].(bool)
		if installed {
			cmdline, err := autostart.Install(homeOverrideOf(d))
			if err != nil {
				SendError(ctx.Res, 500, err.Error())
				return
			}
			SendJSON(ctx.Res, 200, map[string]any{"installed": true, "cmdLine": cmdline})
			return
		}
		if err := autostart.Uninstall(); err != nil {
			SendError(ctx.Res, 500, err.Error())
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"installed": false})
	})

	// —— 备份列表（backups/<agentType>/ 滚动备份 + config.json 自身备份） ——
	rt.Get("/api/backups", func(ctx *RequestContext) {
		root := d.Store.BackupsDir()
		groups := []map[string]any{}
		entries, err := os.ReadDir(root)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() || e.Name() == "config" {
					continue
				}
				files := []map[string]any{}
				dirEntries, _ := os.ReadDir(filepath.Join(root, e.Name()))
				for _, f := range dirEntries {
					if f.IsDir() {
						continue
					}
					info, err := f.Info()
					if err != nil {
						continue
					}
					files = append(files, map[string]any{
						"name":       f.Name(),
						"size":       info.Size(),
						"modifiedAt": info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"),
					})
				}
				sort.Slice(files, func(a, b int) bool {
					return fmt.Sprint(files[a]["name"]) > fmt.Sprint(files[b]["name"])
				})
				groups = append(groups, map[string]any{"agentType": e.Name(), "files": files})
			}
		}
		configBackups := []map[string]any{}
		configEntries, err := os.ReadDir(d.Store.ConfigBackupsDir())
		if err == nil {
			for _, f := range configEntries {
				if f.IsDir() {
					continue
				}
				info, err := f.Info()
				if err != nil {
					continue
				}
				configBackups = append(configBackups, map[string]any{
					"name":       f.Name(),
					"size":       info.Size(),
					"modifiedAt": info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"),
				})
			}
			sort.Slice(configBackups, func(a, b int) bool {
				return fmt.Sprint(configBackups[a]["name"]) > fmt.Sprint(configBackups[b]["name"])
			})
		}
		SendJSON(ctx.Res, 200, map[string]any{"groups": groups, "configBackups": configBackups})
	})

	// —— 恢复备份：回写该 agent 当前配置路径；恢复后清该 agent 的 syncState 哈希，
	// 否则下次同步把回滚内容误判为外部修改弹冲突（plan §5 铁律） ——
	rt.Post("/api/backups/restore", func(ctx *RequestContext) {
		body, err := ctx.Body()
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		name, _ := body["name"].(string)
		if name == "" {
			SendError(ctx.Res, 400, "缺少备份文件名")
			return
		}
		if strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
			SendError(ctx.Res, 400, "非法备份文件名")
			return
		}
		if b, ok := body["config"].(bool); ok && b {
			// 恢复管理配置本身：写回 config.json（校验合法才落盘），并让 daemon 重载
			src := filepath.Join(d.Store.ConfigBackupsDir(), name)
			text, ok2, err := fsutil.ReadTextIfExists(src)
			if err != nil || !ok2 {
				SendError(ctx.Res, 404, "备份不存在")
				return
			}
			if _, err := store.DecodeConfig([]byte(text)); err != nil {
				SendError(ctx.Res, 400, fmt.Sprintf("备份内容非法，拒绝恢复: %v", err))
				return
			}
			if err := fsutil.AtomicWriteFile(d.Store.ConfigPath(), text); err != nil {
				SendError(ctx.Res, 500, err.Error())
				return
			}
			cfg, err := d.Store.Load()
			if err != nil {
				SendError(ctx.Res, 500, fmt.Sprintf("恢复后重载失败: %v", err))
				return
			}
			d.Config = cfg
			SendJSON(ctx.Res, 200, map[string]any{"ok": true, "reloaded": true})
			return
		}
		agentType, _ := body["agentType"].(string)
		if agentType == "" {
			SendError(ctx.Res, 400, "缺少 agentType")
			return
		}
		backupPath := filepath.Join(d.Store.BackupsDir(), agentType, name)
		content, ok2, err := fsutil.ReadTextIfExists(backupPath)
		if err != nil || !ok2 {
			SendError(ctx.Res, 404, "备份不存在")
			return
		}
		target, err := d.agentTargetFile(agentType)
		if err != nil {
			SendError(ctx.Res, 400, err.Error())
			return
		}
		// 恢复前先留一份当前内容的备份（可回退的恢复）
		if _, err := backup.BackupFile(target, agentType, d.Store.BackupsDir(), 10); err != nil {
			d.Logger.Warn("恢复前备份失败（继续恢复）", map[string]any{"err": err.Error()})
		}
		if err := fsutil.AtomicWriteFile(target, content); err != nil {
			SendError(ctx.Res, 500, err.Error())
			return
		}
		delete(d.Config.SyncState, agentType)
		if err := d.SaveConfig(); err != nil {
			SendError(ctx.Res, 500, err.Error())
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"ok": true, "file": target})
	})

	// —— 打开数据目录（explorer / open / xdg-open） ——
	rt.Post("/api/settings/open-dir", func(ctx *RequestContext) {
		var err error
		switch runtime.GOOS {
		case "windows":
			err = exec.Command("explorer", d.Store.HomeDir).Start()
		case "darwin":
			err = exec.Command("open", d.Store.HomeDir).Start()
		default:
			err = exec.Command("xdg-open", d.Store.HomeDir).Start()
		}
		if err != nil {
			SendError(ctx.Res, 500, err.Error())
			return
		}
		SendJSON(ctx.Res, 200, map[string]any{"ok": true})
	})
}

// homeOverrideOf tray/autostart 场景的自启条目目标（daemon 进程内没有全局 flag，读 env）。
func homeOverrideOf(d *Daemon) string {
	if v := os.Getenv("AGENT_CONTROL_HOME"); v != "" {
		return v
	}
	if v := os.Getenv("MCP_MANAGER_HOME"); v != "" {
		return v
	}
	return d.Store.HomeDir
}
