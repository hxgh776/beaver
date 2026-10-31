//go:build !windows

package secretbox

import "os/exec"

func hideWindow(*exec.Cmd) {}
