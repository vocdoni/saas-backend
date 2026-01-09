package errors

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"runtime"

	"go.vocdoni.io/dvote/log"
)

// Error is used by handler functions to wrap errors, assigning a unique error code
// and also specifying which HTTP Status should be used.
type Error struct {
	Err        error  `json:"error"` // Original error
	Code       int    // Error code
	HTTPstatus int    // HTTP status code to return
	LogLevel   string // Log level for this error (defaults to "debug")
	Data       any    // Optional data to include in the error response
}

// MarshalJSON returns a JSON containing Err.Error() and Code. Field HTTPstatus is ignored.
//
// Example output: {"error":"account not found","code":4003}
func (e Error) MarshalJSON() ([]byte, error) {
	// This anon struct is needed to actually include the error string,
	// since it wouldn't be marshaled otherwise. (json.Marshal doesn't call Err.Error())
	return json.Marshal(
		struct {
			Error string `json:"error"`
			Code  int    `json:"code"`
			Data  any    `json:"data,omitempty"`
		}{
			Error: e.Err.Error(),
			Code:  e.Code,
			Data:  e.Data,
		})
}

// UnmarshalJSON parses a JSON containing error, code and optionally data.
//
// Example input: {"error":"account not found","code":4003}
func (e *Error) UnmarshalJSON(data []byte) error {
	// This anon struct is needed to actually set the error string,
	// since it wouldn't be unmarshaled otherwise. (cannot json.Unmarshal string into type error)
	parsed := struct {
		Error string `json:"error"`
		Code  int    `json:"code"`
		Data  any    `json:"data,omitempty"`
	}{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	e.Err = fmt.Errorf("%s", parsed.Error)
	e.Code = parsed.Code
	e.Data = parsed.Data
	return nil
}

// Error returns the Message contained inside the APIerror
func (e Error) Error() string {
	return e.Err.Error()
}

// Unwrap returns the error contained inside
func (e Error) Unwrap() error {
	return e.Err
}

// Is returns true if the Code matches
func (e Error) Is(target error) bool {
	t, ok := target.(Error)
	if !ok {
		tp, ok := target.(*Error)
		if !ok {
			return false
		}
		t = *tp
	}
	return e.Code == t.Code
}

// Write serializes a JSON msg using Error.Err and Error.Code
// and passes that to http.Error(). It also logs the error with appropriate level.
func (e Error) Write(w http.ResponseWriter) {
	msg, err := json.Marshal(e)
	if err != nil {
		log.Warn(err)
		http.Error(w, "marshal failed", http.StatusInternalServerError)
		return
	}

	// Get caller information for better logging
	pc, file, line, _ := runtime.Caller(1)
	caller := runtime.FuncForPC(pc).Name()

	// Log the error with appropriate level
	logLevel := e.LogLevel
	if logLevel == "" {
		// Default log level based on HTTP status
		if e.HTTPstatus >= 500 {
			logLevel = "error"
		} else {
			logLevel = "debug"
		}
	}

	// For 5xx errors, always log with Error level and include internal error details
	if e.HTTPstatus >= 500 {
		// For internal errors, log the full error details
		log.Errorw(e.Err, fmt.Sprintf("API error response [%d]: %s (code: %d, caller: %s, file: %s:%d)",
			e.HTTPstatus, e.Error(), e.Code, caller, file, line))
	} else if log.Level() == log.LogLevelDebug {
		// For 4xx errors, log with debug level
		errMsg := fmt.Sprintf("API error response [%d]: %s (code: %d, caller: %s)",
			e.HTTPstatus, e.Error(), e.Code, caller)

		switch logLevel {
		case "debug":
			log.Debugw(errMsg)
		case "info":
			log.Infow(errMsg)
		case "warn":
			log.Warnw(errMsg)
		default:
			log.Debugw(errMsg) // Default to debug level for unknown log levels
		}
	}

	// Set the content type to JSON
	w.Header().Set("Content-Type", "application/json")
	http.Error(w, string(msg), e.HTTPstatus)
}

// Withf returns a copy of Error with the Sprintf formatted string appended at the end of e.Err
func (e Error) Withf(format string, args ...any) Error {
	return Error{
		Err:        fmt.Errorf("%w: %v", e.Err, fmt.Sprintf(format, args...)),
		Code:       e.Code,
		HTTPstatus: e.HTTPstatus,
		LogLevel:   e.LogLevel,
	}
}

// With returns a copy of Error with the string appended at the end of e.Err
func (e Error) With(s string) Error {
	return Error{
		Err:        fmt.Errorf("%w: %v", e.Err, s),
		Code:       e.Code,
		HTTPstatus: e.HTTPstatus,
		LogLevel:   e.LogLevel,
	}
}

// WithErr returns a copy of Error with err.Error() appended at the end of e.Err
// The original error is preserved for logging purposes
func (e Error) WithErr(err error) Error {
	return Error{
		Err:        fmt.Errorf("%w: %w", e.Err, err),
		Code:       e.Code,
		HTTPstatus: e.HTTPstatus,
		LogLevel:   e.LogLevel,
	}
}

// WithLogLevel returns a copy of Error with the specified log level
func (e Error) WithLogLevel(level string) Error {
	return Error{
		Err:        e.Err,
		Code:       e.Code,
		HTTPstatus: e.HTTPstatus,
		LogLevel:   level,
	}
}

func (e Error) WithData(data any) Error {
	return Error{
		Err:        e.Err,
		Code:       e.Code,
		HTTPstatus: e.HTTPstatus,
		LogLevel:   e.LogLevel,
		Data:       data,
	}
}

// As finds the first error in err's tree that matches target, and if one is found, sets
// target to that error value and returns true. Otherwise, it returns false.
//
// The tree consists of err itself, followed by the errors obtained by repeatedly
// calling its Unwrap() error or Unwrap() []error method. When err wraps multiple
// errors, As examines err followed by a depth-first traversal of its children.
//
// An error matches target if the error's concrete value is assignable to the value
// pointed to by target, or if the error has a method As(any) bool such that
// As(target) returns true. In the latter case, the As method is responsible for
// setting target.
//
// An error type might provide an As method so it can be treated as if it were a
// different error type.
//
// As panics if target is not a non-nil pointer to either a type that implements
// error, or to any interface type.
func As(err error, target any) bool {
	return stderrors.As(err, target)
}

// Is reports whether any error in err's tree matches target.
//
// The tree consists of err itself, followed by the errors obtained by repeatedly
// calling its Unwrap() error or Unwrap() []error method. When err wraps multiple
// errors, Is examines err followed by a depth-first traversal of its children.
//
// An error is considered to match a target if it is equal to that target or if
// it implements a method Is(error) bool such that Is(target) returns true.
//
// An error type might provide an Is method so it can be treated as equivalent
// to an existing error. For example, if MyError defines
//
//	func (m MyError) Is(target error) bool { return target == fs.ErrExist }
//
// then Is(MyError{}, fs.ErrExist) returns true. See [syscall.Errno.Is] for
// an example in the standard library. An Is method should only shallowly
// compare err and the target and not call [Unwrap] on either.
func Is(err error, target error) bool {
	return stderrors.Is(err, target)
}
