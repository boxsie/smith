package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// NativeRegistration describes a compiled-in native tool handler.
type NativeRegistration struct {
	Version string
	Func    NativeFunc
}

func notImplementedNativeFunc(toolID string) NativeFunc {
	return func(ctx context.Context, input json.RawMessage, scope map[string]string, env map[string]string) (json.RawMessage, error) {
		return nil, fmt.Errorf("native tool %q is not implemented yet", toolID)
	}
}

// nativeHandlerCatalog is the single source of truth for compiled native tool
// IDs. v5 replaces these stub registrations with real handlers.
var nativeHandlerCatalog = map[string]NativeRegistration{
	"web.lookup": {
		Version: "web.lookup-v1",
		Func:    webLookupNative,
	},
	"web.fetch": {
		Version: "web.fetch-v1",
		Func:    webFetchNative,
	},
	"web.fetch_markdown": {
		Version: "web.fetch_markdown-v1",
		Func:    webFetchMarkdownNative,
	},
}

// NativeToolIDs returns the registered native tool IDs in sorted order.
func NativeToolIDs() []string {
	ids := make([]string, 0, len(nativeHandlerCatalog))
	for id := range nativeHandlerCatalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func lookupNativeRegistration(id string) (NativeRegistration, bool) {
	reg, ok := nativeHandlerCatalog[id]
	return reg, ok
}
