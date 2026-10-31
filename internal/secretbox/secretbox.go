// Package secretbox 提供 ServerDef.env / headers 的静态加密（S4）。
//
//   - Windows：DPAPI（CurrentUser 作用域，经 PowerShell ProtectedData，无原生依赖），
//     密文形如 `dpapi:v1:<base64>`；
//   - macOS/Linux：明文回退（原样返回），UI 明示。
//
// 与 TS 侧 packages/core/src/secretbox.ts 行为 1:1（同样的 PowerShell 脚本与密文格式，
// 保证两栈能读写同一份 config.json）。
package secretbox

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

const DpapiPrefix = "dpapi:v1:"

func EncryptionBackend() string {
	if runtime.GOOS == "windows" {
		return "dpapi"
	}
	return "plaintext"
}

func IsEncrypted(value string) bool { return strings.HasPrefix(value, DpapiPrefix) }

func EncryptSecret(plain string) (string, error) {
	if plain == "" || IsEncrypted(plain) {
		return plain, nil
	}
	if EncryptionBackend() != "dpapi" {
		return plain, nil
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(plain))
	out, err := runPowerShell(
		"Add-Type -AssemblyName System.Security; "+
			"$b64=[Console]::In.ReadToEnd().Trim(); "+
			"$bytes=[Convert]::FromBase64String($b64); "+
			"$enc=[Security.Cryptography.ProtectedData]::Protect($bytes, $null, [Security.Cryptography.DataProtectionScope]::CurrentUser); "+
			"[Console]::Out.Write([Convert]::ToBase64String($enc))",
		b64,
	)
	if err != nil {
		return "", err
	}
	return DpapiPrefix + out, nil
}

func DecryptSecret(value string) (string, error) {
	if !IsEncrypted(value) {
		return value, nil
	}
	// 输出 base64 再由调用方解码，避免 PowerShell 控制台编码（GBK 等）损坏非 ASCII 字符
	b64, err := runPowerShell(
		"Add-Type -AssemblyName System.Security; "+
			"$b64=[Console]::In.ReadToEnd().Trim(); "+
			"$bytes=[Convert]::FromBase64String($b64); "+
			"$dec=[Security.Cryptography.ProtectedData]::Unprotect($bytes, $null, [Security.Cryptography.DataProtectionScope]::CurrentUser); "+
			"[Console]::Out.Write([Convert]::ToBase64String($dec))",
		value[len(DpapiPrefix):],
	)
	if err != nil {
		return "", err
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func EncryptRecord(record map[string]string) (map[string]string, error) {
	if record == nil {
		return nil, nil
	}
	out := make(map[string]string, len(record))
	for k, v := range record {
		e, err := EncryptSecret(v)
		if err != nil {
			return nil, err
		}
		out[k] = e
	}
	return out, nil
}

func DecryptRecord(record map[string]string) (map[string]string, error) {
	if record == nil {
		return nil, nil
	}
	out := make(map[string]string, len(record))
	for k, v := range record {
		d, err := DecryptSecret(v)
		if err != nil {
			return nil, err
		}
		out[k] = d
	}
	return out, nil
}

func runPowerShell(script, stdin string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	hideWindow(cmd)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("DPAPI 操作失败: %s", strings.TrimSpace(msg))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
