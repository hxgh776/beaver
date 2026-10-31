package discovery

import (
	"github.com/hxgh776/beaver/internal/store"
)

func storeTransport(t string) store.Transport {
	switch t {
	case "stdio":
		return store.TransportStdio
	case "sse":
		return store.TransportSSE
	default:
		return store.TransportHTTP
	}
}

func boolPtr(b bool) *bool { return &b }

func strPtr(s string) *string { return &s }

func strSlicePtr(list []string) *[]string { return &list }

// optArgs 上游 args 数组为空时保持缺省（TS `...(args?.length ? {args} : {})`）。
func optArgs(list []any) *[]string {
	out := strSlice(list)
	if len(out) == 0 {
		return nil
	}
	return &out
}
