package plugin

import "errors"

var (
	ErrNotFound             = errors.New("plugin not found")
	ErrAlreadyInstalled     = errors.New("plugin already installed")
	ErrInvalidPackage       = errors.New("invalid plugin package")
	ErrInvalidPath          = errors.New("invalid plugin path")
	ErrEnableFailed         = errors.New("plugin enable failed")
	ErrDisableFailed        = errors.New("plugin disable failed")
	ErrUninstallFailed      = errors.New("plugin uninstall failed")
	ErrUpgradeFailed        = errors.New("plugin upgrade failed")
	ErrRollbackFailed       = errors.New("plugin rollback failed")
	ErrPermissionDenied     = errors.New("plugin capability denied")
	ErrManagerClosed        = errors.New("plugin manager is closed")
	ErrNotEnabled           = errors.New("plugin not enabled")
	ErrProxyFailed          = errors.New("plugin proxy failed")
	ErrUnsupportedTransport = errors.New("unsupported plugin transport")
)
