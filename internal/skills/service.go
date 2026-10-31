package skills

// SkillsService：SSOT 目录（<homeDir>/skills/<directory>）+ 各 agent skills 目录投影。
// 对应 TS packages/core/src/skills/service.ts；安装/投影/卸载/更新语义 1:1
// （吸收自 cc-switch 的防御性设计：所有权守卫、乐观锁前滚、双重 zip-slip 防线）。

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hxgh776/beaver/internal/store"
)

const (
	skillMD    = "SKILL.md"
	scanDepth  = 6
	backupKeep = 20
)

// ServiceOptions 服务装配。
type ServiceOptions struct {
	// HomeDir 数据目录（SSOT 根 = <HomeDir>/skills）
	HomeDir string
	// GetConfig 返回当前共享配置对象（daemon 持有单一实例）
	GetConfig func() *store.Config
	// SaveConfig 持久化
	SaveConfig func() error
	// AgentRoot agent 主目录（测试传沙箱根；空 = 用户主目录）
	AgentRoot string
	// FetchZip 覆盖下载实现（测试注入内存 ZIP；nil = 真实网络下载）
	FetchZip func(ctx context.Context, url string) ([]byte, error)
}

// CollectionInstallReport 集合安装结果：installed 与 failed 互斥；单个技能失败不中断整批。
type CollectionInstallReport struct {
	Installed []store.InstalledSkill     `json:"installed"`
	Failed    []CollectionInstallFailure `json:"failed"`
}

type CollectionInstallFailure struct {
	Directory string `json:"directory"`
	Reason    string `json:"reason"`
}

// ProjectionStatus 单 agent 的投影状态。
type ProjectionStatus struct {
	AgentType string `json:"agentType"`
	Enabled   bool   `json:"enabled"`
	// Projected 目标目录存在且内容与 SSOT 一致
	Projected bool `json:"projected"`
	// Modified 目标存在但内容不一致（用户自维护或版本漂移）——投影被拒绝保留
	Modified bool `json:"modified,omitempty"`
}

// WithStatus 已安装 Skill + 投影状态。
type WithStatus struct {
	store.InstalledSkill
	Targets []ProjectionStatus `json:"targets"`
}

// UpdateCandidate 待更新技能。
type UpdateCandidate struct {
	SkillID    string `json:"skillId"`
	Directory  string `json:"directory"`
	Repo       string `json:"repo"`
	LocalHash  string `json:"localHash"`
	RemoteHash string `json:"remoteHash"`
	// Missing 本地 SSOT 目录缺失（换机恢复等），必须重装
	Missing bool `json:"missing,omitempty"`
}

// RepoFailure 更新检查失败项。
type RepoFailure struct {
	Repo   string `json:"repo"`
	Reason string `json:"reason"`
}

// UpdateReport 更新检查报告。
type UpdateReport struct {
	Updates   []UpdateCandidate `json:"updates"`
	Failures  []RepoFailure     `json:"failures"`
	CheckedAt string            `json:"checkedAt"`
}

// UninstallResult 卸载结果：preserved = 用户改过而保留的目标目录；backup = 备份目录。
type UninstallResult struct {
	Preserved []string `json:"preserved"`
	Backup    *string  `json:"backup"`
}

// ToggleResult 停用返回 preserved（用户改过的目录原样保留）。
type ToggleResult struct {
	Preserved *string `json:"preserved,omitempty"`
}

// Service 技能管理服务。
type Service struct {
	opts       ServiceOptions
	ssotDir    string
	targetDirs map[string]string
}

func NewService(opts ServiceOptions) *Service {
	return &Service{
		opts:       opts,
		ssotDir:    filepath.Join(opts.HomeDir, "skills"),
		targetDirs: SkillDirsForAgents(opts.AgentRoot),
	}
}

func (s *Service) SkillsDir() string { return s.ssotDir }

func (s *Service) fetchZip(ctx context.Context, target string) ([]byte, error) {
	if s.opts.FetchZip != nil {
		return s.opts.FetchZip(ctx, target)
	}
	return DownloadZip(ctx, target)
}

func (s *Service) skillRow(id string) (store.InstalledSkill, error) {
	for _, row := range s.opts.GetConfig().Skills {
		if row.ID == id {
			return row, nil
		}
	}
	return store.InstalledSkill{}, inputErrf("未安装的 skill: %s", id)
}

// Search skills.sh 搜索。
func (s *Service) Search(ctx context.Context, q string, limit, offset int) (SearchResult, error) {
	return SearchSkillsSh(ctx, q, limit, offset, 10*time.Second)
}

// List 已安装技能与投影状态。
func (s *Service) List() ([]WithStatus, error) {
	out := []WithStatus{}
	for _, row := range s.opts.GetConfig().Skills {
		targets, err := s.projectionStatus(row)
		if err != nil {
			return nil, err
		}
		out = append(out, WithStatus{InstalledSkill: row, Targets: targets})
	}
	return out, nil
}

func (s *Service) projectionStatus(row store.InstalledSkill) ([]ProjectionStatus, error) {
	ssotHash, err := s.ssotHash(row)
	if err != nil {
		return nil, err
	}
	enabledBy := map[string]bool{}
	for _, t := range s.opts.GetConfig().SkillTargets {
		if t.SkillID == row.ID {
			enabledBy[t.AgentType] = t.Enabled
		}
	}
	status := []ProjectionStatus{}
	for _, agentType := range SkillAgentList {
		dir := filepath.Join(s.targetDirs[agentType], row.Directory)
		projected := false
		modified := false
		if dirExists(dir) {
			if ssotHash == nil {
				modified = true // SSOT 缺失但目标在——按用户自维护对待
			} else {
				h, err := ComputeDirHash(dir)
				if err != nil {
					return nil, err
				}
				projected = h == *ssotHash
				modified = !projected
			}
		}
		status = append(status, ProjectionStatus{
			AgentType: agentType,
			Enabled:   enabledBy[agentType],
			Projected: projected,
			Modified:  modified,
		})
	}
	return status, nil
}

func (s *Service) ssotHash(row store.InstalledSkill) (*string, error) {
	dir := filepath.Join(s.ssotDir, row.Directory)
	if !dirExists(dir) {
		return nil, nil
	}
	h, err := ComputeDirHash(dir)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// InstallFromRepo 从 GitHub 仓库安装单个技能。
func (s *Service) InstallFromRepo(ctx context.Context, repo string, skill, branch string, enableFor []string) (store.InstalledSkill, error) {
	ref, err := ValidateRepoRef(repo, firstNonEmpty(branch, "main"))
	if err != nil {
		return store.InstalledSkill{}, err
	}
	if branch == "" {
		ref.Branch, err = s.resolveBranch(ctx, ref.Owner, ref.Name)
		if err != nil {
			return store.InstalledSkill{}, err
		}
	}
	tmp, err := os.MkdirTemp("", "beaver-skillzip-")
	if err != nil {
		return store.InstalledSkill{}, err
	}
	defer os.RemoveAll(tmp)
	sourceDir, rootName, err := s.fetchAndLocate(ctx, tmp, ref.Owner, ref.Name, ref.Branch, skill)
	if err != nil {
		return store.InstalledSkill{}, err
	}
	readmeURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s/%s", ref.Owner, ref.Name, ref.Branch, repoRel(rootName, sourceDir))
	return s.admit(admitInput{
		Source: "github", DirectoryHint: skill, SourceDir: sourceDir,
		Repo: &ref, ReadmeURL: strPtr(readmeURL), EnableFor: enableFor,
	})
}

// InstallFromLocalZip 从本地 ZIP 安装单个技能。
func (s *Service) InstallFromLocalZip(zip []byte, skill string, enableFor []string) (store.InstalledSkill, error) {
	tmp, err := os.MkdirTemp("", "beaver-skillzip-")
	if err != nil {
		return store.InstalledSkill{}, err
	}
	defer os.RemoveAll(tmp)
	if err := ExtractZip(zip, tmp); err != nil {
		return store.InstalledSkill{}, err
	}
	rootName := singleRootDir(tmp)
	root := tmp
	if rootName != "" {
		root = filepath.Join(tmp, rootName)
	}
	scanned, err := ScanSkillDirs(root)
	if err != nil {
		return store.InstalledSkill{}, err
	}
	sourceDir := ResolveSkillSourceDir(root, scanned, skill)
	if sourceDir == "" {
		return store.InstalledSkill{}, inputErrf("ZIP 中未找到包含 SKILL.md 的目录")
	}
	return s.admit(admitInput{
		Source: "local", DirectoryHint: skill, SourceDir: sourceDir, EnableFor: enableFor,
	})
}

// InstallCollectionFromRepo 集合安装：一次归档收编仓库全部含 SKILL.md 的目录。
func (s *Service) InstallCollectionFromRepo(ctx context.Context, repo, branch string, enableFor []string) (CollectionInstallReport, error) {
	ref, err := ValidateRepoRef(repo, firstNonEmpty(branch, "main"))
	if err != nil {
		return CollectionInstallReport{}, err
	}
	if branch == "" {
		ref.Branch, err = s.resolveBranch(ctx, ref.Owner, ref.Name)
		if err != nil {
			return CollectionInstallReport{}, err
		}
	}
	tmp, err := os.MkdirTemp("", "beaver-skillzip-")
	if err != nil {
		return CollectionInstallReport{}, err
	}
	defer os.RemoveAll(tmp)
	zip, err := s.fetchZip(ctx, mustGithubArchiveURL(ref))
	if err != nil {
		return CollectionInstallReport{}, err
	}
	extractDir := filepath.Join(tmp, "x")
	if err := ExtractZip(zip, extractDir); err != nil {
		return CollectionInstallReport{}, err
	}
	rootName := singleRootDir(extractDir)
	root := extractDir
	if rootName != "" {
		root = filepath.Join(extractDir, rootName)
	}
	scanned, err := ScanSkillDirs(root)
	if err != nil {
		return CollectionInstallReport{}, err
	}
	if len(scanned) == 0 {
		return CollectionInstallReport{}, inputErrf("仓库 %s/%s@%s 中未找到 SKILL.md 目录", ref.Owner, ref.Name, ref.Branch)
	}
	entries := make([]admitInput, 0, len(scanned))
	for _, sc := range scanned {
		hint := collectionHint(sc, root, ref.Name)
		readme := fmt.Sprintf("https://github.com/%s/%s/tree/%s/%s", ref.Owner, ref.Name, ref.Branch, repoRel(rootName, sc.Dir))
		entries = append(entries, admitInput{
			DirectoryHint: hint, SourceDir: sc.Dir, ReadmeURL: strPtr(readme),
		})
	}
	return s.admitAll(entries, commonAdmit{Source: "github", Repo: &ref, EnableFor: enableFor}), nil
}

// InstallCollectionFromLocalZip 集合安装：本地 ZIP 一次收编全部技能。
func (s *Service) InstallCollectionFromLocalZip(zip []byte, enableFor []string) (CollectionInstallReport, error) {
	tmp, err := os.MkdirTemp("", "beaver-skillzip-")
	if err != nil {
		return CollectionInstallReport{}, err
	}
	defer os.RemoveAll(tmp)
	if err := ExtractZip(zip, tmp); err != nil {
		return CollectionInstallReport{}, err
	}
	rootName := singleRootDir(tmp)
	root := tmp
	if rootName != "" {
		root = filepath.Join(tmp, rootName)
	}
	scanned, err := ScanSkillDirs(root)
	if err != nil {
		return CollectionInstallReport{}, err
	}
	if len(scanned) == 0 {
		return CollectionInstallReport{}, inputErrf("ZIP 中未找到包含 SKILL.md 的目录")
	}
	fallback := rootName
	if fallback == "" {
		fallback = "skill"
	}
	entries := make([]admitInput, 0, len(scanned))
	for _, sc := range scanned {
		entries = append(entries, admitInput{
			DirectoryHint: collectionHint(sc, root, fallback), SourceDir: sc.Dir,
		})
	}
	return s.admitAll(entries, commonAdmit{Source: "local", EnableFor: enableFor}), nil
}

// admitAll 集合收编循环：同名目录去重（首个胜出，scan 按路径排序保证确定性）；单个失败收集进 failed 继续装。
func (s *Service) admitAll(entries []admitInput, common commonAdmit) CollectionInstallReport {
	installed := []store.InstalledSkill{}
	failed := []CollectionInstallFailure{}
	seen := map[string]struct{}{}
	for _, e := range entries {
		dirName := SanitizeInstallName(e.DirectoryHint)
		if _, dup := seen[dirName]; dup {
			continue
		}
		seen[dirName] = struct{}{}
		e.Source = common.Source
		e.Repo = common.Repo
		e.EnableFor = common.EnableFor
		row, err := s.admit(e)
		if err != nil {
			failed = append(failed, CollectionInstallFailure{Directory: dirName, Reason: err.Error()})
			continue
		}
		installed = append(installed, row)
	}
	return CollectionInstallReport{Installed: installed, Failed: failed}
}

// resolveBranch 分支回退：main → master（skill.rs:3545-3577）。
func (s *Service) resolveBranch(ctx context.Context, owner, name string) (string, error) {
	var lastErr error
	for _, branch := range []string{"main", "master"} {
		if _, err := ValidateRepoRef(owner+"/"+name, branch); err != nil {
			lastErr = err
			continue
		}
		url, err := GithubArchiveURL(owner, name, branch)
		if err != nil {
			lastErr = err
			continue
		}
		if _, err := s.fetchZip(ctx, url); err != nil { // 探测下载即校验存在性
			lastErr = err
			continue
		}
		return branch, nil
	}
	if lastErr == nil {
		lastErr = inputErrf("无法确定分支")
	}
	return "", lastErr
}

func (s *Service) fetchAndLocate(ctx context.Context, tmpRoot, owner, name, branch, skill string) (string, string, error) {
	url, err := GithubArchiveURL(owner, name, branch)
	if err != nil {
		return "", "", err
	}
	zip, err := s.fetchZip(ctx, url)
	if err != nil {
		return "", "", err
	}
	extractDir := filepath.Join(tmpRoot, "x")
	if err := ExtractZip(zip, extractDir); err != nil {
		return "", "", err
	}
	rootName := singleRootDir(extractDir)
	root := extractDir
	if rootName != "" {
		root = filepath.Join(extractDir, rootName)
	}
	scanned, err := ScanSkillDirs(root)
	if err != nil {
		return "", "", err
	}
	sourceDir := ResolveSkillSourceDir(root, scanned, skill)
	if sourceDir == "" {
		hint := ""
		if skill != "" {
			hint = " " + skill + " 对应的"
		}
		return "", "", inputErrf("仓库 %s/%s@%s 中未找到%s SKILL.md 目录", owner, name, branch, hint)
	}
	return sourceDir, rootName, nil
}

type admitInput struct {
	Source        string
	DirectoryHint string
	SourceDir     string
	Repo          *RepoRef
	ReadmeURL     *string
	EnableFor     []string
}

type commonAdmit struct {
	Source    string
	Repo      *RepoRef
	EnableFor []string
}

// admit 把解析出的源目录收编进 SSOT（冲突检查 → 复制 → 哈希 → 落库 → 按需投影）。
func (s *Service) admit(input admitInput) (store.InstalledSkill, error) {
	config := s.opts.GetConfig()
	metadata := ParseSkillMetadata(filepath.Join(input.SourceDir, skillMD))
	base := firstNonEmpty(input.DirectoryHint, metadataName(metadata), repoField(input.Repo, "name"), "skill")
	directory := SanitizeInstallName(base)
	// 复用/冲突判定：同来源（同 repo 坐标，或同为本地无坐标）→ 刷新；异来源占用同目录名 → 报错不覆盖（#3749 语义）
	existingIdx := -1
	for i, s := range config.Skills {
		if s.Directory == directory {
			existingIdx = i
			break
		}
	}
	if existingIdx >= 0 {
		existing := config.Skills[existingIdx]
		sameRepo := false
		if input.Repo != nil {
			sameRepo = existing.RepoOwner != nil && existing.RepoName != nil && existing.Branch != nil &&
				*existing.RepoOwner == input.Repo.Owner && *existing.RepoName == input.Repo.Name && *existing.Branch == input.Repo.Branch
		} else {
			sameRepo = existing.RepoOwner == nil && existing.RepoName == nil && existing.Source == input.Source
		}
		if !sameRepo {
			occupier := "本地/其他来源"
			if existing.RepoOwner != nil && existing.RepoName != nil {
				occupier = fmt.Sprintf(" %s/%s", *existing.RepoOwner, *existing.RepoName)
			}
			return store.InstalledSkill{}, inputErrf("目录名 %s 已被%s占用", directory, occupier)
		}
	}
	ssotTarget := filepath.Join(s.ssotDir, directory)
	if err := CopyDir(input.SourceDir, ssotTarget); err != nil {
		return store.InstalledSkill{}, err
	}
	contentHash, err := ComputeDirHash(ssotTarget)
	if err != nil {
		return store.InstalledSkill{}, err
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	slug := SkillSlug(firstNonEmpty(metadataName(metadata), directory))
	if slug == "" {
		slug = lowerDash(directory)
	}
	var row *store.InstalledSkill
	if existingIdx >= 0 {
		row = &config.Skills[existingIdx]
		row.Name = firstNonEmpty(metadataName(metadata), row.Name)
		row.Description = metadataDescription(metadata)
		row.Source = input.Source
		row.RepoOwner = optStr(repoField(input.Repo, "owner"))
		row.RepoName = optStr(repoField(input.Repo, "name"))
		row.Branch = optStr(repoField(input.Repo, "branch"))
		row.ReadmeURL = input.ReadmeURL
		row.ContentHash = contentHash
		row.UpdatedAt = now
		row.InstalledAt = now // 乐观锁前滚：重装视同新装
	} else {
		id := "skill-" + slug
		config.Skills = append(config.Skills, store.InstalledSkill{
			ID: id, Directory: directory,
			Name:        firstNonEmpty(metadataName(metadata), directory),
			Description: metadataDescription(metadata),
			Source:      input.Source,
			RepoOwner:   optStr(repoField(input.Repo, "owner")),
			RepoName:    optStr(repoField(input.Repo, "name")),
			Branch:      optStr(repoField(input.Repo, "branch")),
			ReadmeURL:   input.ReadmeURL,
			ContentHash: contentHash,
			InstalledAt: now,
			UpdatedAt:   now,
		})
		row = &config.Skills[len(config.Skills)-1]
		// 关联表默认全关：启用是显式动作（与 MCP 分发语义一致）
		for _, agentType := range SkillAgentList {
			config.SkillTargets = append(config.SkillTargets, store.SkillTarget{SkillID: id, AgentType: agentType, Enabled: false})
		}
	}
	for _, agentType := range input.EnableFor {
		if err := s.enableTarget(*row, agentType); err != nil {
			return store.InstalledSkill{}, err
		}
		// 投影即启用：把关联位一并置 true，否则 UI 显示未启用（与 toggle(true) 语义对齐）
		for i := range config.SkillTargets {
			if config.SkillTargets[i].SkillID == row.ID && config.SkillTargets[i].AgentType == agentType {
				config.SkillTargets[i].Enabled = true
			}
		}
	}
	if err := s.opts.SaveConfig(); err != nil {
		return store.InstalledSkill{}, err
	}
	return *row, nil
}

// Toggle 启用/停用投影（永远 copy，不做 symlink；Windows symlink 防御整套省掉）。
func (s *Service) Toggle(skillID, agentType string, enabled bool) (ToggleResult, error) {
	config := s.opts.GetConfig()
	row, err := s.skillRow(skillID)
	if err != nil {
		return ToggleResult{}, err
	}
	// 旧数据可能没有新增 agent 的关联行（skill agent 名单后来扩充）——懒建，而非报错
	targetIdx := -1
	for i := range config.SkillTargets {
		if config.SkillTargets[i].SkillID == skillID && config.SkillTargets[i].AgentType == agentType {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		config.SkillTargets = append(config.SkillTargets, store.SkillTarget{SkillID: skillID, AgentType: agentType, Enabled: false})
		targetIdx = len(config.SkillTargets) - 1
	}
	if config.SkillTargets[targetIdx].Enabled == enabled {
		return ToggleResult{}, nil
	}
	var preserved *string
	if enabled {
		if err := s.enableTarget(row, agentType); err != nil {
			return ToggleResult{}, err
		}
	} else {
		p, err := s.disableTarget(row, agentType)
		if err != nil {
			return ToggleResult{}, err
		}
		preserved = p
	}
	// 无论目标目录是否被保留，启用位都翻转到用户请求的状态
	config.SkillTargets[targetIdx].Enabled = enabled
	if err := s.opts.SaveConfig(); err != nil {
		return ToggleResult{}, err
	}
	return ToggleResult{Preserved: preserved}, nil
}

func (s *Service) enableTarget(row store.InstalledSkill, agentType string) error {
	ssot := filepath.Join(s.ssotDir, row.Directory)
	if !dirExists(ssot) {
		return inputErrf("SSOT 目录缺失: %s", row.Directory)
	}
	ssotHash, err := ComputeDirHash(ssot)
	if err != nil {
		return err
	}
	dest := filepath.Join(s.targetDirs[agentType], row.Directory)
	if dirExists(dest) {
		h, err := ComputeDirHash(dest)
		if err != nil {
			return err
		}
		if h == ssotHash {
			return nil // 已投影且一致 → 幂等
		}
		return inputErrf("目标已存在且内容不同，拒绝覆盖: %s（如确认替换请先卸载旧目录）", dest)
	}
	return ReplaceWithCopy(ssot, dest)
}

func (s *Service) disableTarget(row store.InstalledSkill, agentType string) (*string, error) {
	ssotHash, err := s.ssotHash(row)
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(s.targetDirs[agentType], row.Directory)
	if !dirExists(dest) {
		return nil, nil
	}
	h, err := ComputeDirHash(dest)
	if err != nil {
		return nil, err
	}
	// 只删"能证明是我们投影的"目录；用户改过的原样保留（所有权守卫）
	if ssotHash != nil && h == *ssotHash {
		if err := os.RemoveAll(dest); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return &dest, nil
}

// CheckUpdates 更新检查：按 repo 分组下载远端，比对 SSOT 哈希。
func (s *Service) CheckUpdates(ctx context.Context) (UpdateReport, error) {
	config := s.opts.GetConfig()
	byRepo := map[string][]*store.InstalledSkill{}
	var order []string
	for i := range config.Skills {
		row := &config.Skills[i]
		if row.RepoOwner == nil || row.RepoName == nil {
			continue
		}
		key := fmt.Sprintf("%s/%s@%s", *row.RepoOwner, *row.RepoName, firstNonEmpty(deref(row.Branch), "main"))
		if _, ok := byRepo[key]; !ok {
			order = append(order, key)
		}
		byRepo[key] = append(byRepo[key], row)
	}
	updates := []UpdateCandidate{}
	failures := []RepoFailure{}
	for _, key := range order {
		rows := byRepo[key]
		at := strings.LastIndex(key, "@")
		branch := key[at+1:]
		tmpRoot, err := os.MkdirTemp("", "beaver-skillchk-")
		if err != nil {
			failures = append(failures, RepoFailure{Repo: key, Reason: err.Error()})
			continue
		}
		func() {
			defer os.RemoveAll(tmpRoot)
			sourceDir, rootName, err := s.fetchAndLocate(ctx, tmpRoot, *rows[0].RepoOwner, *rows[0].RepoName, branch, "")
			if err != nil {
				failures = append(failures, RepoFailure{Repo: key, Reason: err.Error()})
				return
			}
			remoteRoot := sourceDir
			if rootName != "" {
				remoteRoot = filepath.Join(filepath.Dir(sourceDir), rootName)
			}
			scanned, err := ScanSkillDirs(remoteRoot)
			if err != nil {
				failures = append(failures, RepoFailure{Repo: key, Reason: err.Error()})
				return
			}
			for _, row := range rows {
				ssotDir := filepath.Join(s.ssotDir, row.Directory)
				var localHash *string
				if dirExists(ssotDir) {
					// 先确认目录存在再算（时序铁律）
					if h, err := ComputeDirHash(ssotDir); err == nil {
						localHash = &h
					}
				}
				remote := matchRemoteSkill(scanned, *row)
				if remote == "" {
					continue
				}
				remoteHash, err := ComputeDirHash(remote)
				if err != nil {
					continue
				}
				if localHash == nil || remoteHash != *localHash {
					local := "missing"
					if localHash != nil {
						local = *localHash
					}
					updates = append(updates, UpdateCandidate{
						SkillID: row.ID, Directory: row.Directory, Repo: key,
						LocalHash: local, RemoteHash: remoteHash, Missing: localHash == nil,
					})
				} else if *localHash != row.ContentHash {
					row.ContentHash = *localHash // 现场算出的哈希回填（skill.rs:1287-1326）
				}
			}
		}()
	}
	_ = s.opts.SaveConfig()
	return UpdateReport{Updates: updates, Failures: failures, CheckedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, nil
}

// Update 应用单个更新：备份旧目录 → 换 SSOT → 重投影我们自己的旧投影（skill.rs:1496-1688）。
func (s *Service) Update(ctx context.Context, skillID string) (store.InstalledSkill, error) {
	row, err := s.skillRow(skillID)
	if err != nil {
		return store.InstalledSkill{}, err
	}
	if row.RepoOwner == nil || row.RepoName == nil {
		return store.InstalledSkill{}, inputErrf("该 skill 无 GitHub 来源，无法更新")
	}
	branch := firstNonEmpty(deref(row.Branch), "main")
	tmp, err := os.MkdirTemp("", "beaver-skillupd-")
	if err != nil {
		return store.InstalledSkill{}, err
	}
	defer os.RemoveAll(tmp)
	sourceDir, _, err := s.fetchAndLocate(ctx, tmp, *row.RepoOwner, *row.RepoName, branch, row.Directory)
	if err != nil {
		return store.InstalledSkill{}, err
	}
	metadata := ParseSkillMetadata(filepath.Join(sourceDir, skillMD))
	newHash, err := ComputeDirHash(sourceDir)
	if err != nil {
		return store.InstalledSkill{}, err
	}
	ssot := filepath.Join(s.ssotDir, row.Directory)
	oldHash, err := s.ssotHash(row)
	if err != nil {
		return store.InstalledSkill{}, err
	}
	if _, err := s.backupSkill(row); err != nil {
		return store.InstalledSkill{}, err
	}
	if dirExists(ssot) {
		if err := os.RemoveAll(ssot); err != nil {
			return store.InstalledSkill{}, err
		}
	}
	if err := CopyDir(sourceDir, ssot); err != nil {
		return store.InstalledSkill{}, err
	}
	// 重新投影：目标哈希等于旧内容 → 是我们的投影，替换；否则保留用户改动
	config := s.opts.GetConfig()
	for _, t := range config.SkillTargets {
		if t.SkillID != skillID || !t.Enabled {
			continue
		}
		dest := filepath.Join(s.targetDirs[t.AgentType], row.Directory)
		if !dirExists(dest) {
			if err := ReplaceWithCopy(ssot, dest); err != nil {
				return store.InstalledSkill{}, err
			}
			continue
		}
		h, err := ComputeDirHash(dest)
		if err != nil {
			continue
		}
		if h == newHash {
			continue
		}
		if oldHash != nil && h == *oldHash {
			if err := os.RemoveAll(dest); err != nil {
				return store.InstalledSkill{}, err
			}
			if err := ReplaceWithCopy(ssot, dest); err != nil {
				return store.InstalledSkill{}, err
			}
		}
	}
	// 回写注册行（config.Skills 中的同一行）
	for i := range config.Skills {
		if config.Skills[i].ID == skillID {
			config.Skills[i].ContentHash = newHash
			if n := metadataName(metadata); n != "" {
				config.Skills[i].Name = n
			}
			config.Skills[i].Description = metadataDescription(metadata)
			now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
			config.Skills[i].InstalledAt = now // 乐观锁前滚
			config.Skills[i].UpdatedAt = now
			row = config.Skills[i]
		}
	}
	if err := s.opts.SaveConfig(); err != nil {
		return store.InstalledSkill{}, err
	}
	return row, nil
}

// Uninstall 卸载：清理投影（只删我们自己的）→ 备份 → 删 SSOT → 删注册行。
func (s *Service) Uninstall(skillID string) (UninstallResult, error) {
	config := s.opts.GetConfig()
	row, err := s.skillRow(skillID)
	if err != nil {
		return UninstallResult{}, err
	}
	// 目录名必须仍是 sanitize 形态——脏行跳过文件操作但仍删注册行（skill.rs:1046-1053）
	safeDir := ""
	if SanitizeInstallName(row.Directory) == row.Directory {
		safeDir = row.Directory
	}
	preserved := []string{}
	var backup *string
	if safeDir != "" {
		backup, err = s.backupSkill(row)
		if err != nil {
			return UninstallResult{}, err
		}
		for _, agentType := range SkillAgentList {
			p, err := s.disableTarget(row, agentType)
			if err != nil {
				return UninstallResult{}, err
			}
			if p != nil && !containsStr(preserved, *p) {
				preserved = append(preserved, *p)
			}
		}
		ssot := filepath.Join(s.ssotDir, safeDir)
		if dirExists(ssot) {
			if err := os.RemoveAll(ssot); err != nil {
				return UninstallResult{}, err
			}
		}
	}
	skills := config.Skills[:0]
	for _, s := range config.Skills {
		if s.ID != skillID {
			skills = append(skills, s)
		}
	}
	config.Skills = skills
	targets := config.SkillTargets[:0]
	for _, t := range config.SkillTargets {
		if t.SkillID != skillID {
			targets = append(targets, t)
		}
	}
	config.SkillTargets = targets
	if err := s.opts.SaveConfig(); err != nil {
		return UninstallResult{}, err
	}
	return UninstallResult{Preserved: preserved, Backup: backup}, nil
}

// backupSkill 卸载/更新前整目录备份：skill-backups/<ts>_<slug>/{skill/, meta.json}，保留 20 份。
func (s *Service) backupSkill(row store.InstalledSkill) (*string, error) {
	source := filepath.Join(s.ssotDir, row.Directory)
	from := ""
	if dirExists(source) {
		from = source
	} else {
		// SSOT 缺失时从任一投影回捞（skill.rs:3859-3891）
		for _, agentType := range SkillAgentList {
			dest := filepath.Join(s.targetDirs[agentType], row.Directory)
			if dirExists(dest) {
				from = dest
				break
			}
		}
	}
	if from == "" {
		return nil, nil
	}
	root := filepath.Join(s.opts.HomeDir, "skill-backups")
	ts := strings.ReplaceAll(strings.ReplaceAll(time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), ":", "-"), ".", "-")
	dest := filepath.Join(root, ts+"_"+row.Directory)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	if err := CopyDir(from, filepath.Join(dest, "skill")); err != nil {
		return nil, err
	}
	meta, _ := json.Marshal(map[string]any{
		"id": row.ID, "directory": row.Directory, "name": row.Name,
		"contentHash": row.ContentHash, "backedUpAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	if err := os.WriteFile(filepath.Join(dest, "meta.json"), meta, 0o644); err != nil {
		return nil, err
	}
	if err := pruneBackups(root, backupKeep); err != nil {
		return nil, err
	}
	return &dest, nil
}

// —— 纯工具 —— //

func dirExists(dir string) bool {
	st, err := withRetry(func() (os.FileInfo, error) { return os.Stat(dir) })
	return err == nil && st.IsDir()
}

// CopyDir 递归复制目录（SSOT 收编与投影共用）。
func CopyDir(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return copyTree(src, dest)
}

func copyTree(src, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dest, e.Name())
		if e.IsDir() {
			if err := copyTree(s, d); err != nil {
				return err
			}
		} else if e.Type().IsRegular() {
			if err := copyFile(s, d); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o644)
}

// ReplaceWithCopy copy → 同父目录 staging → rename，保证目标替换的原子性（skill.rs:2691-2730）。
// Windows 上 rename 可能被杀软/索引器短暂占用目标而抛 EPERM/EBUSY——指数退避重试。
func ReplaceWithCopy(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	staging := filepath.Join(filepath.Dir(dest),
		fmt.Sprintf(".%s.tmp-%d-%s", filepath.Base(dest), os.Getpid(), randHex(6)))
	defer os.RemoveAll(staging)
	if err := copyTree(src, staging); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return withRetryErr(func() error { return os.Rename(staging, dest) })
}

func randHex(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, n)
	for i := range out {
		out[i] = chars[rand.Intn(len(chars))]
	}
	return string(out)
}

func pruneBackups(root string, keep int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) <= keep {
		return nil
	}
	for _, name := range names[:len(names)-keep] {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return err
		}
	}
	return nil
}

var (
	sanitizeSeparators = regexp.MustCompile(`[\\/]+`)
	sanitizeInvalid    = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	sanitizeDots       = regexp.MustCompile(`\.\.+`)
	sanitizeLeadDash   = regexp.MustCompile(`^[-.]+`)
)

// SanitizeInstallName 安装名 sanitize：跨平台显式禁两个分隔符，拒绝 `..` 与首尾点（skill.rs:3181-3185）。
func SanitizeInstallName(raw string) string {
	cleaned := sanitizeSeparators.ReplaceAllString(raw, "-")
	cleaned = sanitizeInvalid.ReplaceAllString(cleaned, "-")
	cleaned = sanitizeDots.ReplaceAllString(cleaned, ".")
	cleaned = sanitizeLeadDash.ReplaceAllString(cleaned, "")
	if len(cleaned) > 80 {
		cleaned = cleaned[:80]
	}
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "skill"
	}
	return cleaned
}

var slugInvalid = regexp.MustCompile(`[^a-z0-9-]+`)

// SkillSlug 展示名 → id slug。
func SkillSlug(raw string) string {
	lower := strings.ToLower(raw)
	lower = slugInvalid.ReplaceAllString(lower, "-")
	lower = strings.Trim(lower, "-")
	if len(lower) > 60 {
		lower = lower[:60]
	}
	return lower
}

func lowerDash(s string) string {
	out := slugInvalid.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(out, "-")
}

// ScannedSkillDir 扫描到的技能目录。
type ScannedSkillDir struct {
	Dir      string
	Metadata *SkillMetadata
}

// SkillMetadata SKILL.md frontmatter 极简解析结果。
type SkillMetadata struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

func metadataName(m *SkillMetadata) string {
	if m == nil {
		return ""
	}
	return m.Name
}

func metadataDescription(m *SkillMetadata) *string {
	if m == nil || m.Description == "" {
		return nil
	}
	return &m.Description
}

// ScanSkillDirs 递归扫描含 SKILL.md 的目录（深度 ≤6）；根目录本身含 SKILL.md 也算；返回按路径排序的确定性列表。
func ScanSkillDirs(root string) ([]ScannedSkillDir, error) {
	out := []ScannedSkillDir{}
	if fileExists(filepath.Join(root, skillMD)) {
		out = append(out, ScannedSkillDir{Dir: root, Metadata: ParseSkillMetadata(filepath.Join(root, skillMD))})
	}
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > scanDepth {
			return nil
		}
		entries, err := withRetry(func() ([]os.DirEntry, error) { return os.ReadDir(dir) })
		if err != nil {
			return nil // Windows 瞬时占用：跳过该层（与 TS catch-return 一致）
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if fileExists(filepath.Join(full, skillMD)) {
				out = append(out, ScannedSkillDir{Dir: full, Metadata: ParseSkillMetadata(filepath.Join(full, skillMD))})
			}
			if err := walk(full, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, 0); err != nil {
		return nil, err
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Dir < out[b].Dir })
	return out, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// SingleRootDir ZIP 归档根目录名（`repo-main`）；无唯一根返回空串。
func singleRootDir(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	dirs := []string{}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 1 && len(entries) == 1 {
		return dirs[0]
	}
	return ""
}

// ResolveSkillSourceDir 4 级解析（skill.rs:3411-3470）：递归同名唯一目录 → 直接路径 →
// metadata name 唯一匹配 → 多个同名取第一个；未指定名时：唯一 skill 或根目录 SKILL.md 兜底，否则取第一个。
func ResolveSkillSourceDir(root string, scanned []ScannedSkillDir, skill string) string {
	if len(scanned) == 0 {
		return ""
	}
	if skill == "" {
		for _, s := range scanned {
			if samePath(s.Dir, root) {
				return root
			}
		}
		return scanned[0].Dir
	}
	byDirName := []ScannedSkillDir{}
	for _, s := range scanned {
		if filepath.Base(s.Dir) == skill {
			byDirName = append(byDirName, s)
		}
	}
	if len(byDirName) == 1 {
		return byDirName[0].Dir
	}
	byMeta := []ScannedSkillDir{}
	for _, s := range scanned {
		if metadataName(s.Metadata) == skill {
			byMeta = append(byMeta, s)
		}
	}
	if len(byMeta) == 1 {
		return byMeta[0].Dir
	}
	if len(byDirName) > 0 {
		return byDirName[0].Dir
	}
	if len(byMeta) > 0 {
		return byMeta[0].Dir
	}
	return ""
}

func matchRemoteSkill(scanned []ScannedSkillDir, row store.InstalledSkill) string {
	for _, s := range scanned {
		if filepath.Base(s.Dir) == row.Directory {
			return s.Dir
		}
	}
	var byMeta []string
	for _, s := range scanned {
		if metadataName(s.Metadata) == row.Name {
			byMeta = append(byMeta, s.Dir)
		}
	}
	if len(byMeta) == 1 {
		return byMeta[0]
	}
	return ""
}

// ParseSkillMetadata 极简 frontmatter 解析：--- 块内的 name/description。
func ParseSkillMetadata(file string) *SkillMetadata {
	text, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	content := string(text)
	if !strings.HasPrefix(content, "---") {
		return nil
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return nil
	}
	meta := &SkillMetadata{}
	for _, line := range strings.Split(content[3:3+end], "\n") {
		m := metaLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		value := strings.TrimSpace(m[2])
		value = strings.Trim(value, `"'`)
		switch m[1] {
		case "name":
			meta.Name = value
		case "description":
			meta.Description = value
		}
	}
	return meta
}

var metaLineRe = regexp.MustCompile(`^(name|description)\s*:\s*(.+)$`)

func repoRel(rootName, sourceDir string) string {
	if rootName == "" {
		return ""
	}
	norm := filepath.ToSlash(sourceDir)
	idx := strings.Index(norm, "/"+rootName+"/")
	if idx < 0 {
		return ""
	}
	return norm[idx+len(rootName)+2:]
}

// collectionHint 集合安装的目录命名：子目录用目录名；根目录自身的 SKILL.md 用 metadata name，兜底仓库名/归档根名。
func collectionHint(s ScannedSkillDir, root, fallback string) string {
	if samePath(s.Dir, root) {
		return firstNonEmpty(metadataName(s.Metadata), fallback)
	}
	return filepath.Base(s.Dir)
}

func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && aa == bb
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func strPtr(s string) *string { return &s }

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// repoField nil 安全的坐标字段读取。
func repoField(ref *RepoRef, field string) string {
	if ref == nil {
		return ""
	}
	switch field {
	case "owner":
		return ref.Owner
	case "name":
		return ref.Name
	case "branch":
		return ref.Branch
	}
	return ""
}

// mustGithubArchiveURL 坐标已过 ValidateRepoRef，此处断言不会失败。
func mustGithubArchiveURL(ref RepoRef) string {
	url, err := GithubArchiveURL(ref.Owner, ref.Name, ref.Branch)
	if err != nil {
		panic(err)
	}
	return url
}

// withRetryErr error 签名的瞬时锁重试。
func withRetryErr(fn func() error) error {
	_, err := withRetry(func() (struct{}, error) { return struct{}{}, fn() })
	return err
}
