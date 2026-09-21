//go:build !linux && !darwin

package localdocker

import "os"

func platformUID() int {
	return -1
}

func fileOwnerUID(os.FileInfo) (int, bool) {
	return 0, false
}
