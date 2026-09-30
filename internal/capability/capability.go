// Package capability defines the runtime-only authority plane for tools granted
// to external model bodies. Patch documents declare intent; conductors provide
// concrete grants when a run starts.
package capability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/boxsie/smith/internal/runtime"
)

const (
	AccessRead   = "read"
	AccessMutate = "mutate"
)

type Grant struct {
	Package string            `json:"package"`
	Access  string            `json:"access"`
	Scope   map[string]string `json:"scope,omitempty"`
}

type Event struct {
	Type         string            `json:"type"`
	Package      string            `json:"package"`
	Access       string            `json:"access"`
	Tool         string            `json:"tool,omitempty"`
	TicketID     string            `json:"ticket_id,omitempty"`
	InvocationID string            `json:"invocation_id"`
	Body         string            `json:"body"`
	Facts        map[string]string `json:"facts,omitempty"`
	IsError      bool              `json:"is_error,omitempty"`
	Error        string            `json:"error,omitempty"`
}

type OpenRequest struct {
	RunID        string
	InvocationID string
	Body         string
	Access       string
	Scope        map[string]string
	Report       func(Event) error
}

type Binding struct {
	Server runtime.MCPServer
	Close  func() error
}

type Provider interface {
	Open(context.Context, OpenRequest) (*Binding, error)
}

type Factory struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewFactory() *Factory {
	return &Factory{providers: make(map[string]Provider)}
}

func (f *Factory) Register(name string, provider Provider) error {
	name = strings.TrimSpace(name)
	if name == "" || provider == nil {
		return fmt.Errorf("capability package name and provider are required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.providers[name]; exists {
		return fmt.Errorf("capability package %q is already registered", name)
	}
	f.providers[name] = provider
	return nil
}

func (f *Factory) Resolve(name string) (Provider, error) {
	if f == nil {
		return nil, fmt.Errorf("capability package %q is unavailable", name)
	}
	f.mu.RLock()
	provider := f.providers[name]
	f.mu.RUnlock()
	if provider == nil {
		return nil, fmt.Errorf("capability package %q is unavailable", name)
	}
	return provider, nil
}

func (f *Factory) Names() []string {
	if f == nil {
		return []string{}
	}
	f.mu.RLock()
	result := make([]string, 0, len(f.providers))
	for name := range f.providers {
		result = append(result, name)
	}
	f.mu.RUnlock()
	sort.Strings(result)
	return result
}

func ParseIntent(value string) (name, access string, err error) {
	name, access, found := strings.Cut(strings.TrimSpace(value), ".")
	if !found || name == "" || !ValidAccess(access) {
		return "", "", fmt.Errorf("capability intent %q must be <package>.read or <package>.mutate", value)
	}
	return name, access, nil
}

func ValidAccess(access string) bool {
	return access == AccessRead || access == AccessMutate
}

func Allows(granted, requested string) bool {
	return granted == requested || granted == AccessMutate && requested == AccessRead
}

func MatchGrant(grants []Grant, name, access string) (Grant, error) {
	for _, grant := range grants {
		if grant.Package == name && Allows(grant.Access, access) {
			copy := grant
			copy.Access = access
			copy.Scope = cloneScope(grant.Scope)
			return copy, nil
		}
	}
	return Grant{}, fmt.Errorf("capability %s.%s was not granted by the conductor", name, access)
}

func CloneGrants(grants []Grant) []Grant {
	result := make([]Grant, len(grants))
	for index, grant := range grants {
		result[index] = grant
		result[index].Scope = cloneScope(grant.Scope)
	}
	return result
}

func ValidateGrants(grants []Grant) error {
	seen := make(map[string]struct{}, len(grants))
	for _, grant := range grants {
		if grant.Package == "" || grant.Package != strings.TrimSpace(grant.Package) || !ValidAccess(grant.Access) {
			return fmt.Errorf("capability grants require a package and read or mutate access")
		}
		if _, exists := seen[grant.Package]; exists {
			return fmt.Errorf("capability package %q was granted more than once", grant.Package)
		}
		seen[grant.Package] = struct{}{}
	}
	return nil
}

func SortedIntents(values []string) ([]string, error) {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	sort.Strings(result)
	for index, value := range result {
		if index > 0 && value == result[index-1] {
			return nil, fmt.Errorf("capability intent %q is duplicated", value)
		}
		if _, _, err := ParseIntent(value); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func cloneScope(scope map[string]string) map[string]string {
	if scope == nil {
		return nil
	}
	result := make(map[string]string, len(scope))
	for key, value := range scope {
		result[key] = value
	}
	return result
}
