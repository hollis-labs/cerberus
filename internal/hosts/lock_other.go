//go:build windows

package hosts

import "errors"

func lockFile(string) (func(), error) {
	return nil, errors.New("editing hosts.yaml is not supported on this platform")
}
