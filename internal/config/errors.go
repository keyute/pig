package config

import "errors"

var (
	ErrNoHome             = errors.New("no Kopi home directory: set KOPI_HOME or HOME")
	ErrUnknownKey         = errors.New("unknown config key")
	ErrInvalidValue       = errors.New("invalid config value")
	ErrUnsupportedVersion = errors.New("unsupported config version")
	ErrInvalidModel       = errors.New("invalid model: want provider/id")
	ErrNoModel            = errors.New(`no model configured: set "model" to provider/id in the config file or pass --model`)
)
