// Package backup 提供写入 agent 配置文件前的滚动备份。
// 对应 TS 侧 packages/core/src/backup.ts。
package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// BackupFile 写入 agent 配置文件前的滚动备份：backupsRoot/<agentType>/<时间戳>-<原名>。
// 文件不存在时返回 ""（无需备份）；超过 keep 份时删除最旧的。
func BackupFile(file, agentType, backupsRoot string, keep int) (string, error) {
	if keep == 0 {
		keep = 10
	}
	content, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	dir := filepath.Join(backupsRoot, agentType)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// TS：new Date().toISOString() → 替换 [:.] → 替换 T → 截 23 字符（UTC，无 Z）
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	ts = filepathSafeTs(ts)
	dest := filepath.Join(dir, fmt.Sprintf("%s-%s", ts, filepath.Base(file)))
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		return "", err
	}
	if err := pruneOld(dir, keep); err != nil {
		return "", err
	}
	return dest, nil
}

// filepathSafeTs 复刻 TS 链式变换：replaceAll(/[:.]/g,'-').replace('T','_').slice(0,23)。
// "2026-10-31T13:53:11.123Z" → "2026-10-31_13-53-11-123"
func filepathSafeTs(iso string) string {
	out := make([]byte, 0, len(iso))
	for i := 0; i < len(iso); i++ {
		switch c := iso[i]; c {
		case ':', '.':
			out = append(out, '-')
		case 'T':
			out = append(out, '_')
		default:
			out = append(out, c)
		}
	}
	if len(out) > 23 {
		out = out[:23]
	}
	return string(out)
}

func pruneOld(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) <= keep {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names) // 固定宽度时间戳前缀，字典序即时间序
	for _, name := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(dir, name))
	}
	return nil
}
