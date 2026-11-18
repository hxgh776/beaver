// multica 侧的「技能库投影通道」与设置构造（VENU-15 重构）。
//
// beaver 把 multica 当成一个普通 agent：技能投影到 multica = 推进 workspace 的
// Skill 库（skill create/update + files upsert/delete）；库以名称为键与 beaver 的
// SSOT 对齐。multica 平台内部把技能分给哪些 agent，不归 beaver 管。
package multica

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hxgh776/beaver/internal/store"
)

// CLIFromSettings 按 settings.multica 构造调用配置（daemon / 适配器共用）。
func CLIFromSettings(ms store.MulticaSettings) *CLI {
	return &CLI{
		Binary:      ms.Binary,
		Profile:     ms.Profile,
		ServerURL:   ms.ServerURL,
		WorkspaceID: ms.WorkspaceID,
	}
}

// SkillLibrarySink 把 beaver 的技能 SSOT 投影到 Multica workspace Skill 库。
// 实现 skills.SkillSink 契约（Push = create-or-update 全量对齐；Remove = 按名移除）。
// CLI() 每次调用现取（连接设置变更即时生效）。
type SkillLibrarySink struct {
	CLI func() *CLI
}

// SkillName 推送/移除使用的库条目名 = beaver 的安装目录名（sanitize 后唯一、稳定）。
func SkillName(row store.InstalledSkill) string { return row.Directory }

func (s *SkillLibrarySink) Push(ctx context.Context, row store.InstalledSkill, ssotDir string) error {
	cli := s.cli()
	if cli == nil {
		return &Error{Code: CodeInvalid, Message: "multica CLI 未配置"}
	}
	name := SkillName(row)
	files, err := listSkillDirFiles(ssotDir)
	if err != nil {
		return &Error{Code: CodeUnknown, Message: "读取技能目录失败", Detail: err.Error()}
	}
	body, description := skillBodyAndDescription(files, row)
	id, err := s.ensureSkill(ctx, cli, name, description, body)
	if err != nil {
		return err
	}
	return s.syncSkillFiles(ctx, cli, id, files)
}

func (s *SkillLibrarySink) Remove(ctx context.Context, row store.InstalledSkill) error {
	cli := s.cli()
	if cli == nil {
		return &Error{Code: CodeInvalid, Message: "multica CLI 未配置"}
	}
	name := SkillName(row)
	skills, err := cli.ListSkills(ctx)
	if err != nil {
		return err
	}
	for _, sk := range skills {
		if sk.Name == name {
			return cli.DeleteSkill(ctx, sk.ID)
		}
	}
	return nil // 远端本就没有 → 幂等成功
}

func (s *SkillLibrarySink) cli() *CLI {
	if s == nil || s.CLI == nil {
		return nil
	}
	return s.CLI()
}

// ensureSkill 按名确保库条目存在并更新内容/描述；返回条目 id。
func (s *SkillLibrarySink) ensureSkill(ctx context.Context, cli *CLI, name, description string, body *dirFile) (string, error) {
	skills, err := cli.ListSkills(ctx)
	if err != nil {
		return "", err
	}
	existing := ""
	for _, sk := range skills {
		if sk.Name == name {
			existing = sk.ID
			break
		}
	}
	contentFile, cleanup := "", func() {}
	if body != nil {
		path, c, ferr := cli.TempFile(body.content, "beaver-skill-*.md")
		if ferr != nil {
			return "", ferr
		}
		contentFile, cleanup = path, c
		defer cleanup()
	}
	if existing != "" {
		if err := cli.UpdateSkill(ctx, existing, "", description, contentFile); err != nil {
			return "", err
		}
		return existing, nil
	}
	// create：只带非空字段（空 --description/--content-file 会被 CLI 当 usage 错误）
	args := []string{"skill", "create", "--name", name}
	if description != "" {
		args = append(args, "--description", description)
	}
	if contentFile != "" {
		args = append(args, "--content-file", contentFile)
	}
	stdout, err := cli.run(ctx, WriteTimeout, args...)
	if err != nil {
		return "", err
	}
	// create 输出容忍非 JSON 文本；解析不出 id 就靠下一次 list（名称唯一可回查）
	if res := parseImportResult(stdout); res != nil && res.ID != "" {
		return res.ID, nil
	}
	fresh, err := cli.ListSkills(ctx)
	if err != nil {
		return "", err
	}
	for _, sk := range fresh {
		if sk.Name == name {
			return sk.ID, nil
		}
	}
	return "", &Error{Code: CodeUnknown, Message: "技能已创建但无法确认 id（列表未回查到）"}
}

// syncSkillFiles 附属文件对齐：SSOT 有的 upsert，远端多出的删除（SKILL.md 即内容本体，不走 files）。
func (s *SkillLibrarySink) syncSkillFiles(ctx context.Context, cli *CLI, skillID string, files []dirFile) error {
	remote, err := cli.ListSkillFiles(ctx, skillID)
	if err != nil {
		return err
	}
	local := map[string]bool{}
	for _, f := range files {
		if f.rel == "SKILL.md" {
			continue
		}
		local[f.rel] = true
		contentFile, cleanup, ferr := cli.TempFile(f.content, "beaver-skillfile-*")
		if ferr != nil {
			return ferr
		}
		err = cli.UpsertSkillFile(ctx, skillID, f.rel, contentFile)
		cleanup()
		if err != nil {
			return fmt.Errorf("附属文件 %s: %w", f.rel, err)
		}
	}
	for _, rf := range remote {
		if rf.Path == "SKILL.md" || rf.Path == "" {
			continue
		}
		if local[rf.Path] {
			continue
		}
		if rf.ID == "" {
			continue // 无 id 无法删除（列表形状差异），保留并接受冗余
		}
		if err := cli.DeleteSkillFile(ctx, skillID, rf.ID); err != nil {
			return fmt.Errorf("删除远端多余文件 %s: %w", rf.Path, err)
		}
	}
	return nil
}

// parseImportResult 解析 skill create/import 的输出（容忍进度前缀）。
func parseImportResult(stdout string) *ImportResult {
	jsonStr := extractJSONObject(stdout)
	if jsonStr == "" {
		return nil
	}
	res := &ImportResult{}
	_ = jsonUnmarshalInto(jsonStr, res)
	return res
}

// —— SSOT 目录读取 —— //

type dirFile struct {
	rel     string // 相对路径（/ 分隔）
	content string
}

var skillDirMaxFiles = 200

// listSkillDirFiles 读取技能目录全部非隐藏文件（相对路径 / 分隔，稳定排序）。
func listSkillDirFiles(dir string) ([]dirFile, error) {
	out := []dirFile{}
	var walk func(cur, rel string) error
	walk = func(cur, rel string) error {
		entries, err := os.ReadDir(cur)
		if err != nil {
			if os.IsNotExist(err) && rel == "" {
				return fmt.Errorf("技能目录不存在: %s", dir)
			}
			return err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			childRel := e.Name()
			if rel != "" {
				childRel = rel + "/" + e.Name()
			}
			abs := filepath.Join(cur, e.Name())
			if e.IsDir() {
				if err := walk(abs, childRel); err != nil {
					return err
				}
				continue
			}
			if !e.Type().IsRegular() {
				continue
			}
			if len(out) >= skillDirMaxFiles {
				return fmt.Errorf("技能文件数超过上限 %d", skillDirMaxFiles)
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return err
			}
			out = append(out, dirFile{rel: filepath.ToSlash(childRel), content: string(data)})
		}
		return nil
	}
	if err := walk(dir, ""); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

var frontmatterLineRe = regexp.MustCompile(`^([A-Za-z_-]+)\s*:\s*(.*)$`)

// skillBodyAndDescription 从文件表取 SKILL.md 正文（剥离 frontmatter）与描述
//（frontmatter 优先，回退注册行）。文件表里没有 SKILL.md 时正文为 nil。
func skillBodyAndDescription(files []dirFile, row store.InstalledSkill) (*dirFile, string) {
	description := ""
	if row.Description != nil {
		description = *row.Description
	}
	for i, f := range files {
		if f.rel != "SKILL.md" {
			continue
		}
		body, meta := splitFrontmatter(f.content)
		if meta["description"] != "" {
			description = meta["description"]
		}
		out := files[i]
		out.content = body
		return &out, description
	}
	return nil, description
}

// splitFrontmatter 剥离文件头部的 YAML frontmatter（--- 界定），返回正文与键值对。
func splitFrontmatter(content string) (string, map[string]string) {
	meta := map[string]string{}
	if !strings.HasPrefix(content, "---") {
		return content, meta
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return content, meta
	}
	for _, line := range strings.Split(content[3:3+end], "\n") {
		if m := frontmatterLineRe.FindStringSubmatch(line); m != nil {
			meta[strings.TrimSpace(m[1])] = strings.Trim(strings.TrimSpace(m[2]), `"'`)
		}
	}
	body := content[3+end+3:]
	body = strings.TrimLeft(body, "\r\n")
	return body, meta
}
