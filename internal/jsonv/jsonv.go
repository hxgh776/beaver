// Package jsonv 实现"保插入序"的 JSON 值树 + 与 JS JSON.stringify / JSON.parse
// 完全同语义的解析与序列化。
//
// 为什么不用 encoding/json 的 map：Go map 键序随机，而写引擎对 agent 配置文件的
// 整文件重写承诺"未管辖内容保真"——键序保真是行为对齐的根基（TS 侧 JS 对象天然
// 保插入序）。数字格式化遵循 ECMA-262 Number::toString（与 JSON.stringify 一致）。
package jsonv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Kind int

const (
	KindNull Kind = iota
	KindBool
	KindNumber
	KindString
	KindArray
	KindObject
)

// Value 是不可变语义上的 JSON 值（通过方法读写）。零值 = null。
type Value struct {
	kind Kind
	b    bool
	n    float64
	s    string
	arr  []*Value
	obj  *Object
}

// Object 是保插入序的字符串键对象。对已存在键 Set 只覆盖值、保持原位置
// （与 JS 对象语义一致）；新键追加到末尾。
type Object struct {
	keys []string
	m    map[string]*Value
}

func Null() *Value          { return &Value{kind: KindNull} }
func NewBool(b bool) *Value { return &Value{kind: KindBool, b: b} }
func NewNumber(f float64) *Value {
	return &Value{kind: KindNumber, n: f}
}
func NewString(s string) *Value { return &Value{kind: KindString, s: s} }
func NewArray(vals ...*Value) *Value {
	return &Value{kind: KindArray, arr: slices.Clone(vals)}
}
func NewObject() *Value {
	return &Value{kind: KindObject, obj: &Object{m: map[string]*Value{}}}
}

func (v *Value) Kind() Kind {
	if v == nil {
		return KindNull
	}
	return v.kind
}

func (v *Value) IsNull() bool { return v.Kind() == KindNull }
func (v *Value) Bool() bool   { return v.b }
func (v *Value) Number() float64 {
	if v == nil || v.kind != KindNumber {
		return 0
	}
	return v.n
}
func (v *Value) String() string {
	if v == nil || v.kind != KindString {
		return ""
	}
	return v.s
}
func (v *Value) Array() []*Value {
	if v == nil || v.kind != KindArray {
		return nil
	}
	return v.arr
}
func (v *Value) Object() *Object {
	if v == nil || v.kind != KindObject {
		return nil
	}
	return v.obj
}

func (v *Value) SetBool(b bool)      { *v = *NewBool(b) }
func (v *Value) SetNumber(f float64) { *v = *NewNumber(f) }
func (v *Value) SetString(s string)  { *v = *NewString(s) }

// IsObject / IsArray —— 类型守卫（null / 数组 / 标量都算"非对象"）。
func (v *Value) IsObject() bool { return v != nil && v.kind == KindObject }

// —— 对象委托方法（仅当值是对象时生效；让调用侧少一层 .Object()） —— //

func (v *Value) Set(key string, val *Value) {
	if v.IsObject() {
		v.obj.Set(key, val)
	}
}

func (v *Value) Get(key string) (*Value, bool) { return v.Object().Get(key) }

func (v *Value) Delete(key string) {
	if v.IsObject() {
		v.obj.Delete(key)
	}
}

func (v *Value) Keys() []string { return v.Object().Keys() }

func (v *Value) Len() int { return v.Object().Len() }

// —— Object —— //

func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// Get 返回键对应值；不存在返回 (nil, false)。
func (o *Object) Get(key string) (*Value, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.m[key]
	return v, ok
}

// Set 覆盖或追加（新键追加到末尾，保持 JS 插入序语义）。
func (o *Object) Set(key string, v *Value) {
	if o.m == nil {
		o.m = map[string]*Value{}
	}
	if _, exists := o.m[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.m[key] = v
}

func (o *Object) Delete(key string) {
	if o == nil {
		return
	}
	if _, ok := o.m[key]; !ok {
		return
	}
	delete(o.m, key)
	o.keys = slices.DeleteFunc(o.keys, func(k string) bool { return k == key })
}

// Clone 深拷贝。
func (o *Object) Clone() *Object {
	if o == nil {
		return nil
	}
	n := &Object{keys: slices.Clone(o.keys), m: make(map[string]*Value, len(o.m))}
	for k, v := range o.m {
		n.m[k] = v.Clone()
	}
	return n
}

func (v *Value) Clone() *Value {
	if v == nil {
		return nil
	}
	n := *v
	if v.arr != nil {
		n.arr = make([]*Value, len(v.arr))
		for i, e := range v.arr {
			n.arr[i] = e.Clone()
		}
	}
	if v.obj != nil {
		n.obj = v.obj.Clone()
	}
	return &n
}

// —— 解析（JSON.parse 同语义：重复键保留首次位置、覆盖值；尾随内容报错） —— //

func Parse(data []byte) (*Value, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("json: 尾随内容")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (*Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeFromToken(dec, tok)
}

func decodeFromToken(dec *json.Decoder, tok json.Token) (*Value, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := NewObject()
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("json: 对象键不是字符串")
				}
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				obj.Object().Set(key, val) // 重复键：覆盖值、保持首次位置（JS 语义）
			}
			if _, err := dec.Token(); err != nil { // 消费 '}'
				return nil, err
			}
			return obj, nil
		case '[':
			arr := NewArray()
			for dec.More() {
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr.Append(val)
			}
			if _, err := dec.Token(); err != nil { // 消费 ']'
				return nil, err
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("json: 意外分隔符 %v", t)
		}
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return nil, err
		}
		return NewNumber(f), nil
	case string:
		return NewString(t), nil
	case bool:
		return NewBool(t), nil
	case nil:
		return Null(), nil
	default:
		return nil, fmt.Errorf("json: 意外 token %T", tok)
	}
}

func (v *Value) Append(vals ...*Value) { v.arr = append(v.arr, vals...) }

// —— 序列化 —— //

// StringifyPretty 等价 JS JSON.stringify(v, null, 2)（不含尾随换行）。
func StringifyPretty(v *Value) string {
	var b strings.Builder
	writeValue(&b, v, 0, true)
	return b.String()
}

// StringifyCompact 等价 JS JSON.stringify(v)（无缩进）。
func StringifyCompact(v *Value) string {
	var b strings.Builder
	writeValue(&b, v, 0, false)
	return b.String()
}

// StringifySorted 递归键排序 + 紧凑序列化，等价 TS stableStringify。
func StringifySorted(v *Value) string {
	return StringifyCompact(sortValue(v))
}

// Canonical 把任意 Go 值按 TS stableStringify 语义序列化：键递归排序、数组保序、
// 紧凑格式。支持 *Value 与原生 map[string]any / []any / string / 各类数值 / bool / nil。
func Canonical(v any) string {
	if val, ok := v.(*Value); ok {
		return StringifySorted(val)
	}
	var b strings.Builder
	writeCanonical(&b, v)
	return b.String()
}

func writeCanonical(b *strings.Builder, v any) {
	switch t := v.(type) {
	case *Value:
		writeCanonical(b, fromValue(t))
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, t)
	case float64:
		b.WriteString(jsFormatNumber(t))
	case float32:
		b.WriteString(jsFormatNumber(float64(t)))
	case int:
		b.WriteString(strconv.FormatInt(int64(t), 10))
	case int8:
		b.WriteString(strconv.FormatInt(int64(t), 10))
	case int16:
		b.WriteString(strconv.FormatInt(int64(t), 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(t), 10))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case uint:
		b.WriteString(strconv.FormatUint(uint64(t), 10))
	case uint8:
		b.WriteString(strconv.FormatUint(uint64(t), 10))
	case uint16:
		b.WriteString(strconv.FormatUint(uint64(t), 10))
	case uint32:
		b.WriteString(strconv.FormatUint(uint64(t), 10))
	case uint64:
		b.WriteString(strconv.FormatUint(t, 10))
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonical(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		if len(t) == 0 {
			b.WriteString("{}")
			return
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			writeCanonical(b, t[k])
		}
		b.WriteByte('}')
	default:
		// 未知类型兜底：走标准库紧凑序列化（禁 HTML 转义，保持与 JS 行为最近）
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			b.WriteString("null")
			return
		}
		b.Write(bytes.TrimRight(buf.Bytes(), "\n"))
	}
}

// ToGo 把原生 Go 值（解析库产出的 map[string]any 等）转为 *Value。
// map 键按排序插入（稳定输出）；未知类型映射为 null。
func ToGo(v any) *Value {
	switch t := v.(type) {
	case *Value:
		return t
	case nil:
		return Null()
	case bool:
		return NewBool(t)
	case float64:
		return NewNumber(t)
	case float32:
		return NewNumber(float64(t))
	case int:
		return NewNumber(float64(t))
	case int64:
		return NewNumber(float64(t))
	case string:
		return NewString(t)
	case []any:
		arr := NewArray()
		for _, e := range t {
			arr.Append(ToGo(e))
		}
		return arr
	case map[string]any:
		o := NewObject()
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			o.Object().Set(k, ToGo(t[k]))
		}
		return o
	default:
		return Null()
	}
}

func fromValue(v *Value) any {
	if v == nil {
		return nil
	}
	switch v.kind {
	case KindBool:
		return v.b
	case KindNumber:
		return v.n
	case KindString:
		return v.s
	case KindArray:
		out := make([]any, 0, len(v.arr))
		for _, e := range v.arr {
			out = append(out, fromValue(e))
		}
		return out
	case KindObject:
		out := make(map[string]any, v.obj.Len())
		for _, k := range v.obj.keys {
			val, _ := v.obj.Get(k)
			out[k] = fromValue(val)
		}
		return out
	default:
		return nil
	}
}

func sortValue(v *Value) *Value {
	if v == nil {
		return Null()
	}
	switch v.kind {
	case KindArray:
		n := NewArray()
		for _, e := range v.arr {
			n.Append(sortValue(e))
		}
		return n
	case KindObject:
		n := NewObject()
		keys := slices.Clone(v.obj.keys)
		slices.Sort(keys)
		for _, k := range keys {
			n.Object().Set(k, sortValue(v.obj.m[k]))
		}
		return n
	default:
		return v.Clone()
	}
}

func writeValue(b *strings.Builder, v *Value, depth int, pretty bool) {
	if v == nil || v.kind == KindNull {
		b.WriteString("null")
		return
	}
	switch v.kind {
	case KindBool:
		if v.b {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case KindNumber:
		b.WriteString(jsFormatNumber(v.n))
	case KindString:
		writeString(b, v.s)
	case KindArray:
		if len(v.arr) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, e := range v.arr {
			if i > 0 {
				b.WriteByte(',')
			}
			if pretty {
				b.WriteString("\n")
				writeIndent(b, depth+1)
			}
			writeValue(b, e, depth+1, pretty)
		}
		if pretty {
			b.WriteString("\n")
			writeIndent(b, depth)
		}
		b.WriteByte(']')
	case KindObject:
		if v.obj.Len() == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, k := range v.obj.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			if pretty {
				b.WriteString("\n")
				writeIndent(b, depth+1)
			}
			writeString(b, k)
			if pretty {
				b.WriteString(": ")
			} else {
				b.WriteByte(':')
			}
			writeValue(b, v.obj.m[k], depth+1, pretty)
		}
		if pretty {
			b.WriteString("\n")
			writeIndent(b, depth)
		}
		b.WriteByte('}')
	}
}

func writeIndent(b *strings.Builder, depth int) {
	for i := 0; i < depth; i++ {
		b.WriteString("  ")
	}
}

// writeString 按 JS JSON.stringify 的字符串转义规则输出：
// 仅转义 `"` `\` 与 <0x20 控制符；U+2028/U+2029 与非 ASCII 原样输出（Go encoding/json
// 默认转义 HTML 与 U+2028/9，这是 TS↔Go 行为对齐的最大隐蔽差异点）。
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
			i++
		case c == '\\':
			b.WriteString(`\\`)
			i++
		case c == '\b':
			b.WriteString(`\b`)
			i++
		case c == '\t':
			b.WriteString(`\t`)
			i++
		case c == '\n':
			b.WriteString(`\n`)
			i++
		case c == '\f':
			b.WriteString(`\f`)
			i++
		case c == '\r':
			b.WriteString(`\r`)
			i++
		case c < 0x20:
			fmt.Fprintf(b, `\u%04x`, c)
			i++
		case c < 0x80:
			b.WriteByte(c)
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				b.WriteString("\ufffd") // 非法字节：JS 输入层同样以 U+FFFD 出现
				i++
				continue
			}
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	b.WriteByte('"')
}

// jsFormatNumber 按 ECMA-262 Number::toString 输出（JSON.stringify 对数字的格式化）：
// 绝对值 < 1e21 的整数用十进制；指数形式无前导零（JS "1e-7"，Go "1e-07"）。
func jsFormatNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == math.Trunc(f) && math.Abs(f) < 9007199254740992 { // 2^53 内整数走 int64 快路径（1e20 会溢出 int64）
		return strconv.FormatInt(int64(f), 10)
	}
	// 最短有效数字：'e' 格式给 d.dddde±XX，拆出有效数字与十进制指数
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa := s
	exp := 0
	if idx := strings.IndexByte(s, 'e'); idx >= 0 {
		mantissa = s[:idx]
		e, err := strconv.Atoi(s[idx+1:])
		if err != nil {
			return s
		}
		exp = e
	}
	neg := strings.HasPrefix(mantissa, "-")
	if neg {
		mantissa = mantissa[1:]
	}
	digits := strings.Replace(mantissa, ".", "", 1)
	n := exp + 1 // ECMA 记号：值 = 0.digits × 10^n
	k := len(digits)
	switch {
	case k <= n && n <= 21:
		return sign(neg) + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign(neg) + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign(neg) + "0." + strings.Repeat("0", -n) + digits
	default:
		e := n - 1
		esign := "+"
		if e < 0 {
			esign = "-"
			e = -e
		}
		body := digits
		if k > 1 {
			body = digits[:1] + "." + digits[1:]
		}
		return sign(neg) + body + "e" + esign + strconv.Itoa(e)
	}
}

func sign(neg bool) string {
	if neg {
		return "-"
	}
	return ""
}
