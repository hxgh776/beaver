package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/hxgh776/beaver/internal/jsonv"
)

// U-ST-01：自动创建父目录并写入；覆盖写不残留临时文件
func TestAtomicWriteFileBasics(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a", "b", "c.json")
	if err := AtomicWriteFile(file, `{"x":1}`); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"x":1}` {
		t.Fatalf("内容不符: %q", got)
	}

	file2 := filepath.Join(dir, "f.json")
	_ = AtomicWriteFile(file2, "v1")
	if err := AtomicWriteFile(file2, "v2"); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(file2)
	if string(got) != "v2" {
		t.Fatalf("覆盖写不符: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("残留临时文件: %s", e.Name())
		}
	}
}

// 并发写同一文件：不丢写、不残留临时文件，落盘为完整内容之一
func TestAtomicWriteFileConcurrentSameFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	if err := AtomicWriteFile(file, "{\"v\":0}\n"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 1; i <= 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = AtomicWriteFile(file, fmt.Sprintf("{\"v\":%d}\n", i))
		}(i)
	}
	wg.Wait()
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\{"v":\d+\}\n$`).Match(got) {
		t.Fatalf("落盘内容不符: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("残留临时文件: %s", e.Name())
		}
	}
}

// U-ST-06 stableHash：键序无关、内容敏感；undefined 字段剔除
func TestStableStringifyAndHash(t *testing.T) {
	o1 := jsonv.NewObject()
	o1.Set("a", jsonv.NewNumber(1))
	inner1 := jsonv.NewObject()
	inner1.Set("c", jsonv.NewNumber(2))
	inner1.Set("d", jsonv.NewNumber(3))
	o1.Set("b", inner1)

	o2 := jsonv.NewObject()
	inner2 := jsonv.NewObject()
	inner2.Set("d", jsonv.NewNumber(3))
	inner2.Set("c", jsonv.NewNumber(2))
	o2.Set("b", inner2)
	o2.Set("a", jsonv.NewNumber(1))

	if StableHash(o1) != StableHash(o2) {
		t.Fatal("键序不同内容相同 → hash 必须一致")
	}
	o3 := jsonv.NewObject()
	o3.Set("a", jsonv.NewNumber(1))
	o4 := jsonv.NewObject()
	o4.Set("a", jsonv.NewNumber(2))
	if StableHash(o3) == StableHash(o4) {
		t.Fatal("内容不同 → hash 必须不同")
	}
	// {x: undefined, y: 1} → {"y":1}
	o5 := jsonv.NewObject()
	o5.Set("y", jsonv.NewNumber(1))
	if got := StableStringify(o5); got != `{"y":1}` {
		t.Fatalf("stableStringify 不符: %q", got)
	}
	// 原生 map 同样支持（键排序）
	native := map[string]any{"b": map[string]any{"d": 3, "c": 2}, "a": 1}
	if StableStringify(native) != `{"a":1,"b":{"c":2,"d":3}}` {
		t.Fatalf("原生 map 序列化不符: %q", StableStringify(native))
	}
}
