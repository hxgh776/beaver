// Package fsutil 提供原子写、确定性序列化与内容哈希。
// 对应 TS 侧 packages/core/src/fsutil.ts，行为 1:1 对齐。
package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/hxgh776/beaver/internal/jsonv"
)

// 同路径写互斥：tmp 名含毫秒时间戳，同毫秒并发写同一文件会生成同名 tmp——
// 先完成的 rename 把文件移走，后一个 rename 必 ENOENT。按目标路径排队串行，
// 配合"内存单实例 + 全量落盘"的保存模式，串行后的 last-write-wins 即正确结果。
var (
	writeMu    sync.Mutex
	writeLocks = map[string]*sync.Mutex{}
)

func writeLock(key string) *sync.Mutex {
	writeMu.Lock()
	defer writeMu.Unlock()
	if l, ok := writeLocks[key]; ok {
		return l
	}
	l := &sync.Mutex{}
	writeLocks[key] = l
	return l
}

// AtomicWriteFile 原子写：临时文件 + 同卷 rename + fsync；成功后目录内不残留临时文件。
// Windows 上 rename 覆盖已有文件可能因目标被短暂占用（杀软/索引器）抛 EPERM/EBUSY，
// 指数退避重试——这是发布到用户机器的必要健壮性。
func AtomicWriteFile(filePath, data string) error {
	key, err := filepath.Abs(filePath)
	if err != nil {
		key = filepath.Clean(filePath)
	}
	l := writeLock(key)
	l.Lock()
	defer l.Unlock()
	return atomicWriteFileInner(filePath, data)
}

func atomicWriteFileInner(filePath, data string) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.%d.tmp", filepath.Base(filePath), os.Getpid(), time.Now().UnixMilli()))
	defer func() { _ = os.Remove(tmp) }()

	if err := os.WriteFile(tmp, []byte(data), 0o644); err != nil {
		return err
	}
	// S0 持久化：rename 前 fsync 临时文件，掉电后意图恢复才成立
	if fh, err := os.OpenFile(tmp, os.O_RDWR, 0); err == nil {
		_ = fh.Sync()
		_ = fh.Close()
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		err := os.Rename(tmp, filePath)
		if err == nil {
			// POSIX 上再 fsync 目录使 rename 本身持久化；Windows 不支持打开目录，跳过
			if runtime.GOOS != "windows" {
				if dh, err := os.Open(dir); err == nil {
					_ = dh.Sync()
					_ = dh.Close()
				}
			}
			return nil
		}
		lastErr = err
		if !transientRenameErr(err) || attempt == 4 {
			break
		}
		time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
	}
	return lastErr
}

// transientRenameErr 对应 TS 侧的 EPERM / EBUSY / EACCES。
// Windows 的原始错误码与 POSIX errno 数值不同，二者都按原始 Errno 值匹配：
//   - 1  = EPERM（POSIX）/ 无直接对应
//   - 5  = ERROR_ACCESS_DENIED（Windows）/ 13 = EACCES（POSIX）
//   - 16 = EBUSY（POSIX）
//   - 32 = ERROR_SHARING_VIOLATION、33 = ERROR_LOCK_VIOLATION（Windows，≈ EBUSY）
func transientRenameErr(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case 1, 5, 13, 16, 32, 33:
			return true
		}
	}
	return false
}

func ReadTextIfExists(filePath string) (string, bool, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

// StableStringify 确定性序列化：对象键递归排序、数组保序。
// 同内容不同键序必须得到相同字符串。支持 *jsonv.Value 与解析产生的原生值
// （map[string]any / []any / string / float64 / int64 / bool / nil）。
func StableStringify(v any) string {
	return jsonv.Canonical(v)
}

// StableHash = SHA-256(StableStringify) 的十六进制小写。
func StableHash(v any) string {
	sum := sha256.Sum256([]byte(StableStringify(v)))
	return hex.EncodeToString(sum[:])
}
