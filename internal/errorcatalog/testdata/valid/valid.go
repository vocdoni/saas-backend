package testpkg

import (
	"fmt"
	"net/http"
)

var ErrFoo = Error{Code: 40001, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("some error")}
