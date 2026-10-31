// Package writeengine 提供首写字节级备份与发布日志（S0 写前意图 + 崩溃前滚）。
// 对应 TS 侧 packages/core/src/write-engine.ts。
package writeengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hxgh776/beaver/internal/fsutil"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sha256String(s string) string { return sha256Hex([]byte(s)) }

// FirstWriteBackup S0-首写备份：每个 agent 配置文件在首次被本工具写入前，留一份
// 字节级原始备份。以绝对路径 hash 为键、`.source` 标记防重——引擎出 bug 时用户仍能
// 找回升级前的原文件。文件不存在（首写时才创建）返回 ""。
func FirstWriteBackup(filePath, backupsRoot string) (string, error) {
	dir := filepath.Join(backupsRoot, "first-write")
	abs, err := filepath.Abs(filePath)
	if err != nil {
		abs = filepath.Clean(filePath)
	}
	key := sha256String(abs)[:24]
	marker := filepath.Join(dir, key+".source")
	if _, err := os.Stat(marker); err == nil {
		return "", nil // 已备份过：只做一次
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // 首写时文件还不存在，无可备份
		}
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	bak := filepath.Join(dir, key+".bak")
	if err := os.WriteFile(bak, content, 0o644); err != nil { // 字节直写，不做编码转换
		return "", err
	}
	meta, _ := json.MarshalIndent(map[string]any{
		"path":       filePath,
		"backedUpAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"bytes":      len(content),
	}, "", "  ")
	if err := os.WriteFile(marker, meta, 0o644); err != nil {
		return "", err
	}
	return bak, nil
}

// —— S0 写前意图（pending publication）+ 崩溃前滚 —— //

// Journal 是适配器可见的最小接口（由 SyncEngine 持有实现，避免循环依赖）。
type Journal interface {
	Record(filePath string, preText *string, plannedText string) error
	Complete(filePath string) error
}

type pendingFileEntry struct {
	Path        string  `json:"path"`
	PreHash     *string `json:"preHash"`
	PlannedHash string  `json:"plannedHash"`
	Done        bool    `json:"done,omitempty"`
}

type pendingPublication struct {
	Version   int                `json:"version"`
	StartedAt string             `json:"startedAt"`
	Files     []pendingFileEntry `json:"files"`
}

// PublicationJournal 发布日志：任何受管文件 rename 之前先落盘意图（pre/planned hash），
// 发布后清账。崩溃后按三态判定（见 RecoverPendingPublications）。
type PublicationJournal struct {
	file  string
	state *pendingPublication
}

func NewPublicationJournal(homeDir string) *PublicationJournal {
	return &PublicationJournal{file: filepath.Join(homeDir, "pending-publication.json")}
}

func (j *PublicationJournal) Record(filePath string, preText *string, plannedText string) error {
	if j.state == nil {
		j.state = &pendingPublication{
			Version:   1,
			StartedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			Files:     []pendingFileEntry{},
		}
	}
	// 同一文件重试（校验失败重写）→ 覆盖旧条目
	kept := j.state.Files[:0]
	for _, f := range j.state.Files {
		if f.Path != filePath {
			kept = append(kept, f)
		}
	}
	j.state.Files = kept
	entry := pendingFileEntry{Path: filePath, PlannedHash: sha256String(plannedText)}
	if preText != nil {
		h := sha256String(*preText)
		entry.PreHash = &h
	}
	j.state.Files = append(j.state.Files, entry)
	return j.persist()
}

func (j *PublicationJournal) Complete(filePath string) error {
	if j.state == nil {
		return nil
	}
	var entry *pendingFileEntry
	for i := range j.state.Files {
		if j.state.Files[i].Path == filePath {
			entry = &j.state.Files[i]
			break
		}
	}
	if entry == nil {
		return nil
	}
	entry.Done = true
	allDone := true
	for _, f := range j.state.Files {
		if !f.Done {
			allDone = false
			break
		}
	}
	if allDone {
		j.state = nil
		return os.Remove(j.file)
	}
	return j.persist()
}

func (j *PublicationJournal) persist() error {
	data, err := json.MarshalIndent(j.state, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(j.file, string(data))
}

// PublicationRecoveryState 三态：unpublished（未发布）/ published（已发布）/
// modified-after-publish（发布后被外部改过——如实上报，不回写）。
type PublicationRecoveryState string

const (
	RecoveryUnpublished          PublicationRecoveryState = "unpublished"
	RecoveryPublished            PublicationRecoveryState = "published"
	RecoveryModifiedAfterPublish PublicationRecoveryState = "modified-after-publish"
)

type PublicationRecovery struct {
	Path  string
	State PublicationRecoveryState
}

// RecoverPendingPublications daemon 启动时调用：判定上次未清账的发布意图，随后删除
// 日志文件（恢复本身不改文件内容）。无日志返回 (nil, nil)；日志损坏挪到 .corrupt-<ts>。
func RecoverPendingPublications(homeDir string) ([]PublicationRecovery, error) {
	file := filepath.Join(homeDir, "pending-publication.json")
	text, ok, err := fsutil.ReadTextIfExists(file)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	var parsed pendingPublication
	if err := json.Unmarshal([]byte(text), &parsed); err != nil || parsed.Files == nil {
		// 日志损坏：挪到旁边留证，从空状态开始
		_ = os.Rename(file, fmt.Sprintf("%s.corrupt-%d", file, time.Now().UnixMilli()))
		return nil, nil
	}
	report := []PublicationRecovery{}
	for _, e := range parsed.Files {
		current, ok, _ := fsutil.ReadTextIfExists(e.Path)
		if !ok || (e.PreHash != nil && sha256String(current) == *e.PreHash) {
			report = append(report, PublicationRecovery{Path: e.Path, State: RecoveryUnpublished})
			continue
		}
		if sha256String(current) == e.PlannedHash {
			report = append(report, PublicationRecovery{Path: e.Path, State: RecoveryPublished})
			continue
		}
		report = append(report, PublicationRecovery{Path: e.Path, State: RecoveryModifiedAfterPublish})
	}
	_ = os.Remove(file)
	return report, nil
}
