package errors

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"

	"go.vocdoni.io/dvote/log"
)

// Error is used by handler functions to wrap errors, assigning a unique error code
// and also specifying which HTTP Status should be used.
type Error struct {
	Err        error  // Original error
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

// Error returns the Message contained inside the APIerror
func (e Error) Error() string {
	return e.Err.Error()
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
		Err:        fmt.Errorf("%w: %v", e.Err, err.Error()),
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
