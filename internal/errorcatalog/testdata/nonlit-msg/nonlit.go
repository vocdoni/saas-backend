package testpkg

import (
	"fmt"
	"net/http"
)

var msg = "runtime message"

// Err uses a variable, not a string literal — the catalog must reject this.
var ErrBad = Error{Code: 40001, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf(msg)}
