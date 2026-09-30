package service

import (
	"github.com/boxsie/smith/internal/app"
)

// InspectApp returns Smith's canonical semantic description of a valid app.
func (s *Service) InspectApp(path string) (*app.Description, error) {
	return app.Engine{Factory: s.factory, ExternalFactory: s.externalFactory, Validate: s.validate}.Inspect(path)
}

// OperateApp applies an all-or-none batch of semantic operations against an
// expected authored-app revision. Dry runs use the same shadow validation and
// diff path as committed operations.
func (s *Service) OperateApp(request app.OperateRequest) (*app.OperateResult, error) {
	return app.Engine{Factory: s.factory, ExternalFactory: s.externalFactory, Validate: s.validate}.Operate(request)
}
