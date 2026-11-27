package testpkg

import (
	"fmt"
	"net/http"
)

// StatusNonExistent does not exist in net/http — the catalog must reject this.
var ErrBad = Error{Code: 40001, HTTPstatus: http.StatusNonExistent, Err: fmt.Errorf("some error")}
