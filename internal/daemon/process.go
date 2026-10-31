package daemon

// 进程管理：PID 文件读写、存活探测、分离式启动与杀进程树。
// 对应 TS packages/server/src/process.ts；平台差异见 *_windows.go / *_other.go。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hxgh776/beaver/internal/fsutil"
)

// PidFilePath 数据目录下的 daemon.pid。
func PidFilePath(homeDir string) string { return filepath.Join(homeDir, "daemon.pid") }

// ReadPid 读取 PID 文件；不存在或非法时返回 0。
func ReadPid(pidPath string) (int, error) {
	text, ok, err := fsutil.ReadTextIfExists(pidPath)
	if err != nil || !ok {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || pid <= 0 {
		return 0, nil
	}
	return pid, nil
}

// RemovePidFile 静默删除 PID 文件。
func RemovePidFile(pidPath string) { _ = os.Remove(pidPath) }
