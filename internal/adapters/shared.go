// 共享的写入计划：依据上次同步 hash 判定冲突，缺省"冲突即跳过"。
// 对应 TS 侧 packages/core/src/adapters/shared.ts。
package adapters

import (
	"github.com/hxgh776/beaver/internal/fsutil"
	"github.com/hxgh776/beaver/internal/jsonv"
	"github.com/hxgh776/beaver/internal/store"
)

func JSONEqual(a, b any) bool {
	return fsutil.StableStringify(a) == fsutil.StableStringify(b)
}

type PlannedChange struct {
	Key    string
	Action string // upsert | remove | none
	// Desired upsert 时要写入的 agent 原生值
	Desired *jsonv.Value
}

type planParams struct {
	CurrentValues map[string]*jsonv.Value
	Writes        []FragmentWrite
	Previous      []store.KeyState
	Resolutions   map[string]ConflictResolution
	Denormalize   func(entry *RawServerEntry) (*jsonv.Value, error)
}

// PlanChanges 冲突判定（C8）：
//   - 上次由本工具写入（previous 有记录）且文件中该片段 hash 已漂移、又不等于本次目标值
//     → 用户手改过 → 冲突；
//   - 无 previous 记录但键已存在（删除方向）→ 键非本工具所写 → 一律视为冲突，绝不静默删除。
func PlanChanges(p planParams) ([]PlannedChange, []ConflictInfo, error) {
	plan := []PlannedChange{}
	conflicts := []ConflictInfo{}

	for _, write := range p.Writes {
		current, exists := p.CurrentValues[write.Key]
		currentHash := ""
		hasCurrent := exists
		if exists {
			currentHash = fsutil.StableHash(current)
		}
		var prev *store.KeyState
		for i := range p.Previous {
			if p.Previous[i].Key == write.Key {
				prev = &p.Previous[i]
				break
			}
		}

		recordConflict := func(expectedHash string) {
			resolution := p.Resolutions[write.Key]
			if resolution == "" {
				resolution = ResolutionSkip
			}
			conflicts = append(conflicts, ConflictInfo{
				Key:          write.Key,
				ExpectedHash: expectedHash,
				CurrentHash:  orAbsent(currentHash, hasCurrent),
				Resolution:   resolution,
			})
		}

		if write.Entry == nil {
			if !exists {
				plan = append(plan, PlannedChange{Key: write.Key, Action: "none"})
				continue
			}
			if prev == nil {
				recordConflict("absent")
				res := resolutionOr(p.Resolutions, write.Key, ResolutionSkip)
				if res == ResolutionOverride {
					plan = append(plan, PlannedChange{Key: write.Key, Action: "remove"})
				} else {
					plan = append(plan, PlannedChange{Key: write.Key, Action: "none"})
				}
				continue
			}
			if prev.Hash != currentHash {
				recordConflict(prev.Hash)
				if p.Resolutions[write.Key] == ResolutionOverride {
					plan = append(plan, PlannedChange{Key: write.Key, Action: "remove"})
				} else {
					plan = append(plan, PlannedChange{Key: write.Key, Action: "none"})
				}
				continue
			}
			plan = append(plan, PlannedChange{Key: write.Key, Action: "remove"})
			continue
		}

		desired, err := p.Denormalize(write.Entry)
		if err != nil {
			return nil, nil, err
		}
		if exists && JSONEqual(desired, current) {
			plan = append(plan, PlannedChange{Key: write.Key, Action: "none", Desired: desired})
			continue
		}
		if prev != nil && prev.Hash != currentHash {
			recordConflict(prev.Hash)
			if p.Resolutions[write.Key] == ResolutionOverride {
				plan = append(plan, PlannedChange{Key: write.Key, Action: "upsert", Desired: desired})
			} else {
				plan = append(plan, PlannedChange{Key: write.Key, Action: "none", Desired: desired})
			}
			continue
		}
		plan = append(plan, PlannedChange{Key: write.Key, Action: "upsert", Desired: desired})
	}

	return plan, conflicts, nil
}

func resolutionOr(m map[string]ConflictResolution, key string, def ConflictResolution) ConflictResolution {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

func orAbsent(currentHash string, hasCurrent bool) string {
	if hasCurrent {
		return currentHash
	}
	return "absent"
}

// FinalizeKeyStates 计划完成后（真实或推演），产出最终 keyStates。
//
// 关键语义：keyStates.hash 永远代表"本工具最后一次写入（或确认无漂移）的内容 hash"。
//   - upsert：记录写入后的 hash；
//   - remove：删除失败/被跳过且存在上次快照 → 保留上次快照（冲突态持续可见）；
//   - none 且当前值与上次快照不一致（用户手改被跳过）→ 保留上次快照，
//     绝不把用户的手改 hash 记为快照，否则冲突会被静默吞掉。
func FinalizeKeyStates(plan []PlannedChange, currentAfter map[string]*jsonv.Value, previous []store.KeyState) []store.KeyState {
	states := []store.KeyState{}
	for _, change := range plan {
		value, exists := currentAfter[change.Key]
		currentHash := ""
		hasCurrent := exists
		if exists {
			currentHash = fsutil.StableHash(value)
		}
		var prev *store.KeyState
		for i := range previous {
			if previous[i].Key == change.Key {
				prev = &previous[i]
				break
			}
		}
		switch change.Action {
		case "upsert":
			if hasCurrent {
				states = append(states, store.KeyState{Key: change.Key, Hash: currentHash})
			}
		case "remove":
			if hasCurrent && prev != nil {
				states = append(states, store.KeyState{Key: change.Key, Hash: prev.Hash})
			}
		default: // none
			if !hasCurrent {
				continue
			}
			if prev != nil && prev.Hash != currentHash {
				states = append(states, store.KeyState{Key: change.Key, Hash: prev.Hash})
			} else {
				states = append(states, store.KeyState{Key: change.Key, Hash: currentHash})
			}
		}
	}
	return states
}
