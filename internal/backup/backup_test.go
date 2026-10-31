package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// U-ST-05 文件不存在 → ""
func TestBackupMissingFile(t *testing.T) {
	sandbox := t.TempDir()
	dest, err := BackupFile(filepath.Join(sandbox, "nope.json"), "claude-code", filepath.Join(sandbox, "backups"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if dest != "" {
		t.Fatalf("不存在文件应返回空: %q", dest)
	}
}

// U-ST-05 备份内容一致，超过 keep 份时滚动删除最旧
func TestBackupRolling(t *testing.T) {
	sandbox := t.TempDir()
	file := filepath.Join(sandbox, "claude.json")
	backupsRoot := filepath.Join(sandbox, "backups")
	_ = os.WriteFile(file, []byte("v1"), 0o644)
	for i := 0; i < 12; i++ {
		_ = os.WriteFile(file, []byte(fmt.Sprintf("v%d", i)), 0o644)
		time.Sleep(3 * time.Millisecond) // 固定宽度时间戳含毫秒，同毫秒连写可能同名覆盖
		if _, err := BackupFile(file, "claude-code", backupsRoot, 10); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(backupsRoot, "claude-code")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 10 {
		t.Fatalf("应保留 10 份: %d", len(files))
	}
	newest := files[len(files)-1].Name()
	if !regexp.MustCompile(`claude\.json$`).MatchString(newest) {
		t.Fatalf("最新备份名不符: %s", newest)
	}
	data, _ := os.ReadFile(filepath.Join(dir, newest))
	if string(data) != "v11" {
		t.Fatalf("最新备份内容应为 v11: %q", data)
	}
}
