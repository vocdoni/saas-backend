package api

import (
	"net/http"
)

// InputValidator is a middleware that validates the request body against the
// model stored in the context. It uses the validator package to validate the model.
func (a *API) InputValidator(next http.Handler) http.Handler {
	return a.validator.InputValidator(next)
}

// validateInputModel is a middleware that adds the model to the request context
// for validation by the InputValidator middleware.
func (a *API) validateInputModel(model interface{}) func(http.Handler) http.Handler {
	return a.validator.AddModelMiddleware(model)
}
