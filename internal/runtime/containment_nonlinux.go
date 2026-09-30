//go:build !linux

package runtime

import (
	"context"
	"fmt"
)

func probeHostContainment(context.Context) (string, error) {
	return "", fmt.Errorf("this host has no proven contained execution mechanism")
}
