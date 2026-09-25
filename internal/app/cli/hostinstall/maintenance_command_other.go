//go:build !linux

package hostinstall

import (
	"context"
	"errors"
	"io"
)

func runNative(context.Context, string, NativeRequest, string, string, string, io.Reader, io.Writer) error {
	return errors.New("demo provider upgrades require Linux")
}

func checkMaintenancePlan(context.Context, NativeRequest) error {
	return errors.New("operator maintenance requires Linux")
}
