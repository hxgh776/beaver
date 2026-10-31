package gateway

// 测试辅助：JSON 解码与请求体序列化。

import (
	"bytes"
	"encoding/json"
	"io"
)

func marshalBody(body any) io.Reader {
	if body == nil {
		return nil
	}
	data, _ := json.Marshal(body)
	return bytes.NewReader(data)
}

func jsonDecode(r io.Reader, target any) error {
	return json.NewDecoder(r).Decode(target)
}
