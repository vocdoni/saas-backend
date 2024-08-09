package db

import "fmt"

var (
	ErrNotFound    = fmt.Errorf("not found")
	ErrInvalidData = fmt.Errorf("invalid data provided")
)
