package tool

import "errors"

var (
	ErrOutsideWorkspace = errors.New("path is outside the workspace")
	ErrProtected        = errors.New("path is protected")
	ErrBinary           = errors.New("file is binary")
	ErrIsDirectory      = errors.New("path is a directory")
	ErrNotRegular       = errors.New("path is not a regular file")
	ErrInvalidArgs      = errors.New("invalid arguments")
)
