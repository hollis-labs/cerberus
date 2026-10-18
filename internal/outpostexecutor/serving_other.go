//go:build !linux

package outpostexecutor

import "fmt"

func servingSystemdUnits() ([]string, error) { return nil, fmt.Errorf("systemd unavailable") }
