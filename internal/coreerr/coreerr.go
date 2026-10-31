// Package coreerr 定义 core 层的错误类型（对应 TS src/errors.ts）。
package coreerr

import "fmt"

// CoreError 带错误码的 core 层错误基类。
type CoreError struct {
	Message string
	Code    string
}

func (e *CoreError) Error() string { return e.Message }

// MalformedConfigError agent 配置文件损坏（JSON/TOML 解析失败）——禁止写入，必须显式处理。
type MalformedConfigError struct {
	FilePath string
	Cause    error
}

func (e *MalformedConfigError) Error() string {
	return fmt.Sprintf("agent 配置文件无法解析: %s", e.FilePath)
}
func (e *MalformedConfigError) Unwrap() error { return e.Cause }
func (e *MalformedConfigError) Code() string  { return "MALFORMED_CONFIG" }

// WriteVerificationError 写入后复读校验失败——已回滚。
type WriteVerificationError struct {
	FilePath string
}

func (e *WriteVerificationError) Error() string {
	return fmt.Sprintf("写入后校验失败，已回滚: %s", e.FilePath)
}
func (e *WriteVerificationError) Code() string { return "WRITE_VERIFICATION_FAILED" }

// ConfigConflictError 配置冲突。
type ConfigConflictError struct {
	Message string
}

func (e *ConfigConflictError) Error() string { return e.Message }
func (e *ConfigConflictError) Code() string  { return "CONFIG_CONFLICT" }
