package multica

import (
	"encoding/json"
	"strings"
)

// unwrapArray CLI 的 list 输出兼容层：顶层数组直接用；对象则找第一个是数组的
// 已知包装键（servers/items/data）。multica CLI 的 JSON 形状以实际输出为准，
// 这里容忍包装差异，避免形状微调把面板打断。
func unwrapArray(raw json.RawMessage, keys ...string) ([]map[string]any, error) {
	trimmed := trimSpaceBytes(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []map[string]any{}, nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(trimmed, &arr); err == nil {
		return arr, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, &Error{Code: CodeUnknown, Message: "multica CLI 返回的列表形状无法解析", Detail: truncate(string(trimmed), 200)}
	}
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			var arr2 []map[string]any
			if err := json.Unmarshal(v, &arr2); err == nil {
				return arr2, nil
			}
		}
	}
	return nil, &Error{Code: CodeUnknown, Message: "multica CLI 返回的列表形状无法解析", Detail: truncate(string(trimmed), 200)}
}

// firstString 依次取第一个存在的字符串字段。
func firstString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// firstBool 依次取第一个存在的布尔字段（缺省 false）。
func firstBool(obj map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			if b, ok := v.(bool); ok {
				return b
			}
		}
	}
	return false
}

func trimSpaceBytes(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpaceByte(b[start]) {
		start++
	}
	for end > start && isSpaceByte(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// extractJSONObject 从混合输出（进度行前缀 + JSON 体）中截取第一段平衡的花括号 JSON。
func extractJSONObject(text string) string {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	escape := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if escape {
			escape = false
			continue
		}
		switch c {
		case '\\':
			if inStr {
				escape = true
			}
		case '"':
			inStr = !inStr
		case '{':
			if !inStr {
				depth++
			}
		case '}':
			if !inStr {
				depth--
				if depth == 0 {
					return text[start : i+1]
				}
			}
		}
	}
	return ""
}

// jsonUnmarshalInto 尽力解析；失败静默（导入结果缺省靠刷新列表兜底）。
func jsonUnmarshalInto(text string, target any) error {
	return json.Unmarshal([]byte(text), target)
}
