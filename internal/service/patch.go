package service

import "github.com/boxsie/smith/internal/patch"

// InspectPatch returns the canonical live-patch description and both its
// document and semantic-topology revisions.
func (s *Service) InspectPatch(path string) (*patch.Description, error) {
	return (patch.Engine{}).Inspect(path)
}

// OperatePatch applies an atomic, revision-checked batch to a live patch.
// MCP and visual clients share this service operation rather than editing
// patch.yaml directly.
func (s *Service) OperatePatch(request patch.OperateRequest) (*patch.OperateResult, error) {
	return (patch.Engine{}).Operate(request)
}
