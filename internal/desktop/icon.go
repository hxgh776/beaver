package desktop

// 托盘图标：内嵌品牌 logo（PNG-in-ICO，Vista+ 支持），免 cgo、免外部资源文件。

import (
	"bytes"
	"embed"
	"encoding/binary"
)

//go:embed assets/tray-icon.png
var trayIconPNG []byte

//go:embed assets
var assetsFS embed.FS

// TrayIconICO 生成 ICO 字节（单条目包装内嵌 PNG；512px 宽高写 0 = 256+ 语义）。
func TrayIconICO() []byte {
	png := trayIconPNG
	var buf bytes.Buffer
	// ICONDIR
	buf.WriteByte(0)                                   // reserved
	buf.WriteByte(0)                                   // type 高字节（0 = icon）
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // type = 1 (icon)
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // count
	// ICONDIRENTRY
	buf.WriteByte(0)                                    // width（0 = 256）
	buf.WriteByte(0)                                    // height
	buf.WriteByte(0)                                    // 色板数
	buf.WriteByte(0)                                    // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))  // planes
	binary.Write(&buf, binary.LittleEndian, uint16(32)) // bitcount
	binary.Write(&buf, binary.LittleEndian, uint32(len(png)))
	binary.Write(&buf, binary.LittleEndian, uint32(6+16)) // 数据偏移
	buf.Write(png)
	return buf.Bytes()
}
