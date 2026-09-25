//go:build !linux

package demoupgrade

import (
	"context"
	"errors"
	"io"
)

func runNative(context.Context, string, NativeRequest, string, string, string, io.Reader, io.Writer) error {
	return errors.New("demo provider upgrades require Linux")
}
