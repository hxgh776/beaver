package multica

// 测试二进制自举：BEAVER_FAKE_MULTICA=1 时本进程被当作 multica CLI 重入。

import (
	"os"
	"testing"

	"github.com/hxgh776/beaver/internal/multica/multicafake"
)

func TestMain(m *testing.M) {
	if os.Getenv(multicafake.Env) == "1" {
		os.Exit(multicafake.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}
