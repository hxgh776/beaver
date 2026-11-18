package multica

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// ErrorCode 失败语义分类（VENU-15 设计点 3）：面板据此展示可诊断的状态与修复建议。
type ErrorCode string

const (
	CodeCLIMissing  ErrorCode = "CLI_MISSING"       // multica CLI 未安装 / 路径错
	CodeNotLoggedIn ErrorCode = "NOT_LOGGED_IN"     // CLI 未登录（multica login）
	CodePermission  ErrorCode = "PERMISSION_DENIED" // 需要 workspace admin/owner
	CodeUnreachable ErrorCode = "UNREACHABLE"       // 网络 / 服务端不可达 / 超时
	CodeNotFound    ErrorCode = "NOT_FOUND"         // 目标条目不存在
	CodeConflict    ErrorCode = "CONFLICT"          // 重名冲突等
	CodeInvalid     ErrorCode = "INVALID_INPUT"     // 本地参数非法（发送前已拦截或服务端校验拒绝）
	CodeUnknown     ErrorCode = "UNKNOWN"
)

// Error 可诊断错误：Message 给人看，Hint 是修复建议，Detail 是 stderr 摘要（截断）。
type Error struct {
	Code    ErrorCode
	Message string
	Hint    string
	Detail  string
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s（%s）", e.Message, e.Detail)
	}
	return e.Message
}

// HTTPStatus 错误码 → HTTP 状态（daemon 层使用）。
func (e *Error) HTTPStatus() int {
	switch e.Code {
	case CodeCLIMissing:
		return 503
	case CodeNotLoggedIn:
		return 401
	case CodePermission:
		return 403
	case CodeUnreachable:
		return 502
	case CodeNotFound:
		return 404
	case CodeConflict:
		return 409
	case CodeInvalid:
		return 400
	default:
		return 500
	}
}

var (
	reLogin      = regexp.MustCompile(`(?i)not logged in|please log ?in|login required|unauthenticated|unauthorized|no auth token|session expired`)
	rePermission = regexp.MustCompile(`(?i)admin|owner only|permission|forbidden|403|access denied|not allowed`)
	reNotFound   = regexp.MustCompile(`(?i)not found|404|no such|does not exist|unknown (server|agent|skill)`)
	reConflict   = regexp.MustCompile(`(?i)conflict|already exists|duplicate|409|in use`)
	reNetwork    = regexp.MustCompile(`(?i)could not resolve|refused|timeout|unreachable|network|connect|dns|tls|certificate|reset|EOF|502|503|504|server error|500|too many requests|429`)
	reInvalid    = regexp.MustCompile(`(?i)invalid|required|missing (field|value)|must be|cannot be empty|usage:`)
)

// classify 把一次失败的 exec 归入可诊断类别。优先级：CLI 缺失 > 超时 > 登录 > 权限 >
// 不存在 > 冲突 > 网络 > 参数 > 其他。匹配基于 stderr/stdout 文本 + 错误类型。
func classify(runErr error, stderr string, args []string) error {
	detail := truncate(stderr, 300)
	joined := stderr
	if pe, ok := runErr.(*exec.ExitError); ok {
		_ = pe // 退出码非 0；文本判定足够，退出码不作细分
	} else if errors.Is(runErr, exec.ErrNotFound) {
		return &Error{Code: CodeCLIMissing, Message: "找不到 multica CLI", Hint: "安装 multica CLI 后重试", Detail: detail}
	} else if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
		return &Error{Code: CodeUnreachable, Message: "multica CLI 调用超时", Hint: "检查网络后重试；导入大归档可能需要更久", Detail: detail}
	} else if execErr, ok := runErr.(*exec.Error); ok {
		return &Error{Code: CodeCLIMissing, Message: fmt.Sprintf("无法启动 multica CLI: %s", execErr.Err), Hint: "确认 multica CLI 已安装且可执行", Detail: detail}
	}

	lower := strings.ToLower(joined)
	switch {
	case reLogin.MatchString(lower):
		return &Error{Code: CodeNotLoggedIn, Message: "multica CLI 未登录", Hint: "在终端运行 multica login 完成登录后重试", Detail: detail}
	case rePermission.MatchString(lower):
		return &Error{Code: CodePermission, Message: "权限不足（该操作需要 workspace admin/owner）", Hint: "让 workspace 管理员执行，或换有权限的账号登录 multica", Detail: detail}
	case reNotFound.MatchString(lower):
		return &Error{Code: CodeNotFound, Message: "目标条目在 Multica 上不存在（可能已被删除）", Hint: "刷新列表后重试", Detail: detail}
	case reConflict.MatchString(lower):
		return &Error{Code: CodeConflict, Message: "Multica 端冲突（重名或已绑定）", Hint: "换个名称，或选择覆盖/跳过策略后重试", Detail: detail}
	case reNetwork.MatchString(lower):
		return &Error{Code: CodeUnreachable, Message: "Multica 服务不可达", Hint: "检查网络与 multica login 状态；可在「连接」中检查服务器地址设置", Detail: detail}
	case reInvalid.MatchString(lower):
		return &Error{Code: CodeInvalid, Message: "请求被 Multica 拒绝（参数或格式问题）", Detail: detail}
	}
	cmd := ""
	if len(args) > 0 {
		cmd = strings.Join(args, " ")
	}
	return &Error{Code: CodeUnknown, Message: "multica CLI 调用失败", Hint: "查看 Detail 中的 CLI 输出定位", Detail: truncate(cmd+" → "+stderr, 300)}
}
