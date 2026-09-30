package cli

import (
	"os"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/capability/ticketsplease"
	"github.com/boxsie/smith/internal/contextsource"
	contextrenderer "github.com/boxsie/smith/internal/contextsource/renderer"
	memoryrenderer "github.com/boxsie/smith/internal/memorysource/renderer"
	"github.com/boxsie/smith/internal/service"
	worktickets "github.com/boxsie/smith/internal/worksource/ticketsplease"
)

// smithService is the shared application boundary used by CLI commands. MCP
// and future visual clients construct the same service with their own adapters.
var smithService = service.New(service.Dependencies{
	CapabilityFactory: defaultCapabilityFactory(),
	ContextFactory:    defaultContextFactory(),
	WorkSource:        defaultWorkSource(),
	MemorySource:      defaultMemorySource(),
})

func defaultCapabilityFactory() *capability.Factory {
	factory := capability.NewFactory()
	endpoint := os.Getenv("SMITH_TICKETS_PLEASE_ENDPOINT")
	if endpoint == "" {
		return factory
	}
	_ = factory.Register(ticketsplease.Package, ticketsplease.New(ticketsplease.Config{
		Endpoint: endpoint, BearerToken: os.Getenv("SMITH_TICKETS_PLEASE_BEARER_TOKEN"),
	}))
	return factory
}

func defaultWorkSource() *worktickets.Reader {
	endpoint := os.Getenv("SMITH_TICKETS_PLEASE_ENDPOINT")
	if endpoint == "" {
		return nil
	}
	return worktickets.New(worktickets.Config{
		Endpoint: endpoint, BootstrapProject: os.Getenv("SMITH_TICKETS_PLEASE_PROJECT"),
		BearerToken: os.Getenv("SMITH_TICKETS_PLEASE_BEARER_TOKEN"),
	})
}

func defaultMemorySource() *memoryrenderer.Reader {
	endpoint := os.Getenv("SMITH_MEMORY_ENDPOINT")
	if endpoint == "" {
		return nil
	}
	return memoryrenderer.New(memoryrenderer.Config{
		Endpoint: endpoint, BearerToken: os.Getenv("SMITH_MEMORY_BEARER_TOKEN"),
	})
}

func defaultContextFactory() *contextsource.Factory {
	factory := contextsource.NewFactory()
	memoryDir := os.Getenv("SMITH_MEMORY_DIR")
	if memoryDir == "" {
		return factory
	}
	_ = factory.Register(contextrenderer.Source, contextrenderer.New(contextrenderer.Config{
		Executable: os.Getenv("SMITH_MEMORY_RENDERER"), MemoryDir: memoryDir,
	}))
	return factory
}
