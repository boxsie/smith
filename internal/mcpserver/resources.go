package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (a *Adapter) addResources(server *mcp.Server) {
	server.AddResource(&mcp.Resource{URI: "smith://system/summary", Name: "smith-summary", Title: "Smith control-plane summary", MIMEType: "text/markdown", Description: "Stable operating model and safety boundary for conductor LLMs."}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: request.Params.URI, MIMEType: "text/markdown", Text: Summary}}}, nil
	})
	server.AddResource(&mcp.Resource{URI: "smith://apps", Name: "smith-apps", Title: "Known Smith apps", MIMEType: "application/json", Description: "The persisted app index shared by every local MCP session."}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		index, err := a.projects()
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(index)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: request.Params.URI, MIMEType: "application/json", Text: string(data)}}}, nil
	})
}
