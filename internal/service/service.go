// Package service exposes Smith's application operations independently of any
// transport or user interface. CLI, MCP, and visual clients should all adapt
// this boundary rather than reimplementing orchestration.
package service

import (
	"context"
	"sync"
	"time"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/contextsource"
	"github.com/boxsie/smith/internal/memorysource"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/plan"
	"github.com/boxsie/smith/internal/projects"
	"github.com/boxsie/smith/internal/proposal"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/tools"
	"github.com/boxsie/smith/internal/validate"
	"github.com/boxsie/smith/internal/worksource"
	"github.com/boxsie/smith/internal/workspace"
)

// Event is a transport-neutral service lifecycle notification. The durable,
// detailed run event model is deliberately left to the asynchronous-run phase;
// this hook gives that work a stable injection point without coupling the
// service to a particular event store.
type Event struct {
	At        time.Time
	Operation string
	State     string
	AppRoot   string
	RunID     string
	Err       error
}

// EventSink receives service lifecycle events.
type EventSink interface {
	Emit(context.Context, Event)
}

// EventSinkFunc adapts a function to EventSink.
type EventSinkFunc func(context.Context, Event)

func (f EventSinkFunc) Emit(ctx context.Context, event Event) {
	f(ctx, event)
}

// Dependencies are the replaceable edges of the Smith application service.
// New fills omitted values with production defaults and performs no I/O.
type Dependencies struct {
	Factory           *runtime.Factory
	ExternalFactory   *runtime.ExternalFactory
	CapabilityFactory *capability.Factory
	ContextFactory    *contextsource.Factory
	WorkSource        worksource.Reader
	MemorySource      memorysource.Reader
	Workspace         *workspace.Manager
	RegistryFactory   func() *tools.Registry
	Clock             func() time.Time
	NewRunID          func() string
	TrackProject      func(string) error
	Events            EventSink
	Validate          func(string, *runtime.Factory, *runtime.ExternalFactory) *validate.Result
	Plan              func(context.Context, plan.PlanInput) (*plan.PlanResult, error)
	Apply             func(proposal.ApplyInput) (*proposal.ApplyResult, error)
	CheckRunner       runtime.ProcessRunner
	CheckAdmitter     runtime.ContainmentAdmitter
}

// Service owns Smith application orchestration. It is safe and side-effect
// free to construct; operations perform I/O only when called.
type Service struct {
	factory               *runtime.Factory
	externalFactory       *runtime.ExternalFactory
	capabilityFactory     *capability.Factory
	contextFactory        *contextsource.Factory
	workSource            worksource.Reader
	memorySource          memorysource.Reader
	workspace             *workspace.Manager
	registryFactory       func() *tools.Registry
	clock                 func() time.Time
	newRunID              func() string
	trackProject          func(string) error
	events                EventSink
	validate              func(string, *runtime.Factory, *runtime.ExternalFactory) *validate.Result
	plan                  func(context.Context, plan.PlanInput) (*plan.PlanResult, error)
	apply                 func(proposal.ApplyInput) (*proposal.ApplyResult, error)
	checkRunner           runtime.ProcessRunner
	checkAdmitter         runtime.ContainmentAdmitter
	acquireRunLease       func(string) (runLease, error)
	runsMu                sync.Mutex
	runs                  map[string]*activeRun
	patchesMu             sync.Mutex
	patches               map[string]*patchrun.Scheduler
	patchGrants           map[string][]string
	patchCapabilityGrants map[string][]capability.Grant
	patchUncontained      map[string]bool
	gatesMu               sync.Mutex
	gates                 map[string]*gateWaiter
	harnessMu             sync.Mutex
	harnessPreviews       map[string]*preparedHarness
}

// New constructs a Smith application service without touching the filesystem,
// network, providers, or event sink.
func New(deps Dependencies) *Service {
	if deps.Factory == nil {
		deps.Factory = runtime.DefaultFactory()
	}
	if deps.ExternalFactory == nil {
		deps.ExternalFactory = runtime.DefaultExternalFactory()
	}
	if deps.CapabilityFactory == nil {
		deps.CapabilityFactory = capability.NewFactory()
	}
	if deps.ContextFactory == nil {
		deps.ContextFactory = contextsource.NewFactory()
	}
	if deps.Workspace == nil {
		deps.Workspace = workspace.New("")
	}
	if deps.RegistryFactory == nil {
		deps.RegistryFactory = defaultRegistry
	}
	if deps.Clock == nil {
		deps.Clock = time.Now
	}
	if deps.NewRunID == nil {
		deps.NewRunID = run.NewRunID
	}
	if deps.TrackProject == nil {
		deps.TrackProject = projects.Track
	}
	if deps.Validate == nil {
		deps.Validate = validate.ValidateWithFactories
	}
	if deps.Plan == nil {
		deps.Plan = plan.Plan
	}
	if deps.Apply == nil {
		deps.Apply = proposal.Apply
	}
	if deps.CheckRunner == nil {
		deps.CheckRunner = runtime.OSProcessRunner{}
	}
	if deps.CheckAdmitter == nil {
		deps.CheckAdmitter = runtime.HostContainmentAdmitter{}
	}

	return &Service{
		factory:               deps.Factory,
		externalFactory:       deps.ExternalFactory,
		capabilityFactory:     deps.CapabilityFactory,
		contextFactory:        deps.ContextFactory,
		workSource:            deps.WorkSource,
		memorySource:          deps.MemorySource,
		workspace:             deps.Workspace,
		registryFactory:       deps.RegistryFactory,
		clock:                 deps.Clock,
		newRunID:              deps.NewRunID,
		trackProject:          deps.TrackProject,
		events:                deps.Events,
		validate:              deps.Validate,
		plan:                  deps.Plan,
		apply:                 deps.Apply,
		checkRunner:           deps.CheckRunner,
		checkAdmitter:         deps.CheckAdmitter,
		acquireRunLease:       acquireRunLease,
		runs:                  make(map[string]*activeRun),
		patches:               make(map[string]*patchrun.Scheduler),
		patchGrants:           make(map[string][]string),
		patchCapabilityGrants: make(map[string][]capability.Grant),
		patchUncontained:      make(map[string]bool),
		gates:                 make(map[string]*gateWaiter),
		harnessPreviews:       make(map[string]*preparedHarness),
	}
}

func defaultRegistry() *tools.Registry {
	registry := tools.NewRegistry()
	registry.Register("project.list", &tools.ProjectList{})
	registry.Register("project.read", &tools.ProjectRead{})
	registry.Register("project.find", &tools.ProjectFind{})
	registry.Register("proposal.write", &tools.ProposalWrite{})
	return registry
}

func (s *Service) CapabilityPackages() []string {
	return s.capabilityFactory.Names()
}

func (s *Service) ContextSources() []string { return s.contextFactory.Names() }

func (s *Service) emit(ctx context.Context, event Event) {
	if s.events == nil {
		return
	}
	event.At = s.clock().UTC()
	s.events.Emit(ctx, event)
}

func (s *Service) track(appRoot string) []error {
	if err := s.trackProject(appRoot); err != nil {
		return []error{err}
	}
	return nil
}
