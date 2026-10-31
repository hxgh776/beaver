package jsonv

import "testing"

// 与 JS JSON.stringify/JSON.parse 的行为对齐抽查：键序保真、转义规则、数字格式。
func TestParseStringifyRoundTripParity(t *testing.T) {
	src := "{\n  \"b\": 1,\n  \"a\": \"<x>&\\n中\",\n  \"arr\": [\n    true,\n    null,\n    2.5\n  ]\n}"
	v, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := StringifyPretty(v); got != src {
		t.Fatalf("往返不一致:\n got=%q\nwant=%q", got, src)
	}
	// 键序保持插入序（b 在 a 前）
	keys := v.Object().Keys()
	if len(keys) != 3 || keys[0] != "b" || keys[1] != "a" || keys[2] != "arr" {
		t.Fatalf("键序不符: %v", keys)
	}
	// JS：\u4e2d 解码后 stringify 输出原样字符（不再转义）
	escaped, err := Parse([]byte(`{"a":"\u4e2d"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := StringifyCompact(escaped); got != `{"a":"中"}` {
		t.Fatalf("\\u 解码后应原样输出: %q", got)
	}
}

func TestStringEscapingMatchesJS(t *testing.T) {
	v := NewString("<a>&\"q\"\\é中\n\u0001")
	// JS JSON.stringify：不转义 < > & 与非 ASCII；控制字符用 \u00xx 小写十六进制
	want := "\"<a>&\\\"q\\\"\\\\é中\\n\\u0001\""
	if got := StringifyCompact(v); got != want {
		t.Fatalf("转义不符:\n got=%q\nwant=%q", got, want)
	}
}

func TestNumberFormattingMatchesJS(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1, "1"},
		{6280, "6280"},
		{2.5, "2.5"},
		{0.000001, "0.000001"},          // 1e-6 → 定点
		{1e-7, "1e-7"},                  // 1e-7 → 指数（无前导零）
		{1e21, "1e+21"},                 // ≥1e21 → 指数
		{1e20, "100000000000000000000"}, // <1e21 整数 → 全数字
	}
	for _, c := range cases {
		if got := jsFormatNumber(c.in); got != c.want {
			t.Errorf("jsFormatNumber(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDuplicateKeyKeepsFirstPositionLastValue(t *testing.T) {
	v, err := Parse([]byte(`{"a":1,"b":2,"a":3}`))
	if err != nil {
		t.Fatal(err)
	}
	keys := v.Object().Keys()
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("重复键应保留首次位置: %v", keys)
	}
	a, _ := v.Object().Get("a")
	if a.Number() != 3 {
		t.Fatalf("重复键应保留最后值: %v", a.Number())
	}
}

func TestTrailingContentRejected(t *testing.T) {
	if _, err := Parse([]byte(`{} {}`)); err == nil {
		t.Fatal("尾随内容应报错（JSON.parse 同语义）")
	}
	if _, err := Parse([]byte(``)); err == nil {
		t.Fatal("空输入应报错")
	}
}
