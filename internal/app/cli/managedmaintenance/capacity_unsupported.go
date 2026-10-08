//go:build !linux

package managedmaintenance

import "errors"

func measureFilesystemCapacity(string) (filesystemCapacity, error) {
	return filesystemCapacity{}, errors.New("managed filesystem capacity measurement requires Linux")
}
