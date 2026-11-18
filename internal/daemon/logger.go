// Package daemon 承载 HTTP 服务（/api + 网关端点挂载 + Web UI 静态托管）与配置内存态。
// 对应 TS packages/server/src/{daemon,router,http-util,logger,process,api}.ts。
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type LogLevel string

const (
	LevelDebug LogLevel = "debug"
	LevelInfo  LogLevel = "info"
	LevelWarn  LogLevel = "warn"
	LevelError LogLevel = "error"
)

func levelOrder(l LogLevel) int {
	switch l {
	case LevelDebug:
		return 10
	case LevelInfo:
		return 20
	case LevelWarn:
		return 30
	case LevelError:
		return 40
	}
	return 20
}

// Logger 极简文件+控制台日志：daemon.log 追加写，密钥脱敏交给调用方（绝不打印 env/headers）。
type Logger struct {
	mu       sync.Mutex
	minLevel int
	logFile  string
}

func NewLogger(logFile string, level LogLevel) *Logger {
	if level == "" {
		level = LevelInfo
	}
	return &Logger{minLevel: levelOrder(level), logFile: logFile}
}

func (l *Logger) SetLevel(level string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.minLevel = levelOrder(LogLevel(level))
}

func (l *Logger) Debug(msg string, extra ...map[string]any) {
	l.write(LevelDebug, msg, firstExtra(extra))
}
func (l *Logger) Info(msg string, extra ...map[string]any) {
	l.write(LevelInfo, msg, firstExtra(extra))
}
func (l *Logger) Warn(msg string, extra ...map[string]any) {
	l.write(LevelWarn, msg, firstExtra(extra))
}
func (l *Logger) Error(msg string, extra ...map[string]any) {
	l.write(LevelError, msg, firstExtra(extra))
}

func firstExtra(extra []map[string]any) map[string]any {
	if len(extra) > 0 {
		return extra[0]
	}
	return nil
}

func (l *Logger) write(level LogLevel, msg string, extra map[string]any) {
	ord := levelOrder(level)
	l.mu.Lock()
	defer l.mu.Unlock()
	if ord < l.minLevel {
		return
	}
	line := fmt.Sprintf("%s [%s] %s%s", time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"), strings.ToUpper(string(level)), msg, formatExtra(extra))
	switch level {
	case LevelError:
		fmt.Fprintln(os.Stderr, line)
	case LevelWarn:
		fmt.Fprintln(os.Stderr, line)
	default:
		fmt.Println(line)
	}
	if l.logFile == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.logFile), 0o755); err != nil {
		return // 日志写失败不影响主流程
	}
	f, err := os.OpenFile(l.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}

func formatExtra(extra map[string]any) string {
	if len(extra) == 0 {
		return ""
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(extra))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, extra[k]))
	}
	return " " + strings.Join(parts, " ")
}
