//go:build windows

package sdn

import "go.uber.org/zap"

func newDataPlane(iface string, log *zap.Logger) (DataPlane, error) {
	return NewWindowsDataPlane(iface)
}
