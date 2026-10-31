package main

// autostart 子命令：薄包装，核心逻辑在 internal/autostart（daemon 进程内复用）。

import (
	"fmt"

	"github.com/hxgh776/beaver/internal/autostart"
)

func cmdAutostart(args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "on":
		cmdline, err := autostart.Install(homeOverride)
		if err != nil {
			return err
		}
		fmt.Println("已安装开机自启，下次登录自动拉起 daemon")
		fmt.Printf("  %s\n", cmdline)
		fmt.Println("  撤销: beaver autostart off")
		return nil
	case "off":
		if err := autostart.Uninstall(); err != nil {
			return err
		}
		fmt.Println("已移除开机自启")
		return nil
	case "status":
		if autostart.IsInstalled() {
			fmt.Println("已安装")
		} else {
			fmt.Println("未安装（beaver autostart on 安装）")
		}
		return nil
	default:
		return fmt.Errorf("未知子命令: autostart %s", sub)
	}
}
