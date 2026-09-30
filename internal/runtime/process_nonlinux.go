//go:build !linux

package runtime

import (
	"context"
	"fmt"
)

func prepareProcessLaunch(_ context.Context, request ProcessRequest) (processLaunch, error) {
	if request.Containment.Mechanism == ContainmentDirect &&
		request.Containment.RequestedProfile == ExecutionProfileUncontainedDevelopment {
		return directProcessLaunch(request), nil
	}
	return processLaunch{}, fmt.Errorf("%w: external runtimes require a proven host process boundary", ErrProcessContainmentUnavailable)
}
