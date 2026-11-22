package multica

import (
	"context"
	"strings"
)

// Skill workspace Skill 库条目（multica skill list 的 snake_case 输出 1:1）。
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
	CreatedBy   string   `json:"createdBy"`
}

// ListSkills 列出 workspace Skill 库。
func (c *CLI) ListSkills(ctx context.Context) ([]Skill, error) {
	raw, err := c.runRaw(ctx, ListTimeout, "skill", "list", "--output", "json")
	if err != nil {
		return nil, err
	}
	items, err := unwrapArray(raw, "skills", "items", "data")
	if err != nil {
		return nil, err
	}
	out := make([]Skill, 0, len(items))
	for _, it := range items {
		s := Skill{
			ID:          firstString(it, "id", "skill_id", "skillId"),
			Name:        firstString(it, "name"),
			Description: firstString(it, "description"),
			CreatedAt:   firstString(it, "created_at", "createdAt"),
			UpdatedAt:   firstString(it, "updated_at", "updatedAt"),
			CreatedBy:   firstString(it, "created_by", "createdBy"),
		}
		if labels, ok := it["labels"].([]any); ok {
			for _, l := range labels {
				if str, ok := l.(string); ok {
					s.Labels = append(s.Labels, str)
				}
			}
		}
		if s.ID == "" && s.Name == "" {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// SkillSearchResult 技能搜索结果（可导入源）。
type SkillSearchResult struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      string   `json:"source"`
	URL         string   `json:"url"`
	Repo        *string  `json:"repo"`
	Installs    *float64 `json:"install_count"`
}

// SearchSkills 在可安装源中搜索技能。
func (c *CLI) SearchSkills(ctx context.Context, query string) ([]SkillSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, &Error{Code: CodeInvalid, Message: "搜索关键词不能为空"}
	}
	raw, err := c.runRaw(ctx, ListTimeout, "skill", "search", query, "--output", "json")
	if err != nil {
		return nil, err
	}
	items, err := unwrapArray(raw, "results", "skills", "items", "data")
	if err != nil {
		return nil, err
	}
	out := make([]SkillSearchResult, 0, len(items))
	for _, it := range items {
		r := SkillSearchResult{
			Name:        firstString(it, "name"),
			Description: firstString(it, "description"),
			Source:      firstString(it, "source"),
			URL:         firstString(it, "url"),
		}
		if v, ok := it["repo"].(string); ok && v != "" {
			r.Repo = &v
		}
		if v, ok := it["install_count"].(float64); ok {
			r.Installs = &v
		}
		out = append(out, r)
	}
	return out, nil
}

// OnConflict 导入重名冲突策略（multica skill import 的原值）。
const (
	ConflictFail      = "fail"
	ConflictOverwrite = "overwrite"
	ConflictRename    = "rename"
	ConflictSkip      = "skip"
)

func validConflict(s string) bool {
	switch s {
	case ConflictFail, ConflictOverwrite, ConflictRename, ConflictSkip, "":
		return true
	}
	return false
}

// ImportResult 导入结果（id 缺省时依赖 list 刷新）。
type ImportResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ImportSkillFromURL 从 URL（clawhub.ai / skills.sh / github.com）导入技能。
func (c *CLI) ImportSkillFromURL(ctx context.Context, url, onConflict string) (*ImportResult, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, &Error{Code: CodeInvalid, Message: "导入 URL 不能为空"}
	}
	if !validConflict(onConflict) {
		return nil, &Error{Code: CodeInvalid, Message: "onConflict 非法（fail/overwrite/rename/skip）"}
	}
	args := []string{"skill", "import", "--url", url}
	if onConflict != "" {
		args = append(args, "--on-conflict", onConflict)
	}
	return c.finishImport(ctx, args)
}

// ImportSkillFromFile 从本地归档（.skill/.zip）导入。content 由调用方写临时文件后传入路径。
func (c *CLI) ImportSkillFromFile(ctx context.Context, filePath, onConflict string) (*ImportResult, error) {
	if filePath == "" {
		return nil, &Error{Code: CodeInvalid, Message: "缺少归档文件"}
	}
	if !validConflict(onConflict) {
		return nil, &Error{Code: CodeInvalid, Message: "onConflict 非法（fail/overwrite/rename/skip）"}
	}
	args := []string{"skill", "import", "--file", filePath}
	if onConflict != "" {
		args = append(args, "--on-conflict", onConflict)
	}
	return c.finishImport(ctx, args)
}

func (c *CLI) finishImport(ctx context.Context, args []string) (*ImportResult, error) {
	stdout, err := c.run(ctx, ImportTimeout, args...)
	if err != nil {
		return nil, err
	}
	res := &ImportResult{}
	// 导入输出容忍非 JSON 文本（进度等前缀）：找第一段花括号 JSON
	jsonStr := extractJSONObject(stdout)
	if jsonStr == "" {
		return res, nil // 无法解析也视为成功（具体条目靠刷新列表）
	}
	_ = jsonUnmarshalInto(jsonStr, res)
	return res, nil
}

// CreateSkill 在 workspace 库创建技能。contentFile 是 SKILL.md 内容的临时文件路径。
func (c *CLI) CreateSkill(ctx context.Context, name, description, contentFile string) error {
	if strings.TrimSpace(name) == "" {
		return &Error{Code: CodeInvalid, Message: "技能名称不能为空"}
	}
	args := []string{"skill", "create", "--name", name}
	if description != "" {
		args = append(args, "--description", description)
	}
	if contentFile != "" {
		args = append(args, "--content-file", contentFile)
	}
	_, err := c.run(ctx, WriteTimeout, args...)
	return err
}

// UpdateSkill 更新技能（字段为空 = 不改）。
func (c *CLI) UpdateSkill(ctx context.Context, id, name, description, contentFile string) error {
	if id == "" {
		return &Error{Code: CodeInvalid, Message: "缺少 skill id"}
	}
	args := []string{"skill", "update", id}
	if name != "" {
		args = append(args, "--name", name)
	}
	if description != "" {
		args = append(args, "--description", description)
	}
	if contentFile != "" {
		args = append(args, "--content-file", contentFile)
	}
	if len(args) == 3 {
		return &Error{Code: CodeInvalid, Message: "更新至少需要 name/description/content 之一"}
	}
	_, err := c.run(ctx, WriteTimeout, args...)
	return err
}

// DeleteSkill 删除技能（--yes 免交互确认）。
func (c *CLI) DeleteSkill(ctx context.Context, id string) error {
	if id == "" {
		return &Error{Code: CodeInvalid, Message: "缺少 skill id"}
	}
	_, err := c.run(ctx, WriteTimeout, "skill", "delete", id, "--yes")
	return err
}

// SkillFile 技能附属文件。
type SkillFile struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ListSkillFiles 列出技能文件（诊断与文件级收敛用）。
func (c *CLI) ListSkillFiles(ctx context.Context, skillID string) ([]SkillFile, error) {
	if skillID == "" {
		return nil, &Error{Code: CodeInvalid, Message: "缺少 skill id"}
	}
	raw, err := c.runRaw(ctx, ListTimeout, "skill", "files", "list", skillID, "--output", "json")
	if err != nil {
		return nil, err
	}
	items, err := unwrapArray(raw, "files", "items", "data")
	if err != nil {
		return nil, err
	}
	out := make([]SkillFile, 0, len(items))
	for _, it := range items {
		f := SkillFile{
			ID:   firstString(it, "id", "file_id", "fileId"),
			Path: firstString(it, "path", "name", "file"),
		}
		if v, ok := it["size"].(float64); ok {
			f.Size = int64(v)
		}
		out = append(out, f)
	}
	return out, nil
}

// UpsertSkillFile 创建/更新技能的一个附属文件（contentFile 为文件内容临时路径）。
func (c *CLI) UpsertSkillFile(ctx context.Context, skillID, path, contentFile string) error {
	if skillID == "" || path == "" {
		return &Error{Code: CodeInvalid, Message: "缺少 skill id 或文件路径"}
	}
	_, err := c.run(ctx, WriteTimeout, "skill", "files", "upsert", skillID, "--path", path, "--content-file", contentFile)
	return err
}

// DeleteSkillFile 删除技能的一个附属文件（按 files list 返回的 file id）。
func (c *CLI) DeleteSkillFile(ctx context.Context, skillID, fileID string) error {
	if skillID == "" || fileID == "" {
		return &Error{Code: CodeInvalid, Message: "缺少 skill id 或 file id"}
	}
	_, err := c.run(ctx, WriteTimeout, "skill", "files", "delete", skillID, fileID)
	return err
}
