package simulator

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/yeboahd24/ussd-lab/internal/appclient"
	"github.com/yeboahd24/ussd-lab/internal/session"
)

// API error codes returned to the simulator UI.
//
// They mirror the internal error taxonomies rather than collapsing everything
// into "something went wrong": the whole point of the simulator is telling the
// developer WHERE a failure happened -- transport, session, or their own
// application (provider design §49).
const (
	CodeBadRequest         = "BAD_REQUEST"
	CodeUnknownServiceCode = "UNKNOWN_SERVICE_CODE"
	CodeSessionNotFound    = "SESSION_NOT_FOUND"
	CodeSessionTimeout     = "SESSION_TIMEOUT"
	CodeSessionNotActive   = "SESSION_NOT_ACTIVE"
	CodeInvalidInput       = "INVALID_INPUT"
	CodeApplicationFailure = "APPLICATION_FAILURE"
	CodeAttachRequired     = "ATTACH_REQUIRED"
	CodeInternal           = "SIMULATOR_ERROR"
)

// apiError is a transport-level failure with an HTTP status attached.
type apiError struct {
	Status  int
	Code    string
	Message string
	Hint    string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func newAPIError(status int, code, message, hint string) *apiError {
	return &apiError{Status: status, Code: code, Message: message, Hint: hint}
}

// errorBody is the JSON shape of a failure.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// writeError maps any error onto an HTTP status and a coded JSON body.
//
// The mapping is the contract the phone UI and the e2e tests both rely on, so
// it lives in one place rather than being scattered through handlers.
func writeError(w http.ResponseWriter, err error) {
	status, detail := classifyError(err)

	writeJSON(w, status, errorBody{Error: detail})
}

func classifyError(err error) (int, errorDetail) {
	// Transport-level errors already carry their status.
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.Status, errorDetail{
			Code:    apiErr.Code,
			Message: apiErr.Message,
			Hint:    apiErr.Hint,
		}
	}

	// Session-lifecycle errors.
	switch {
	case errors.Is(err, session.ErrSessionNotFound):
		return http.StatusNotFound, errorDetail{
			Code:    CodeSessionNotFound,
			Message: "session not found",
			Hint:    "dial the service code again to start a new session",
		}

	case errors.Is(err, session.ErrSessionTimedOut):
		return http.StatusGone, errorDetail{
			Code:    CodeSessionTimeout,
			Message: "session timed out",
			Hint:    "USSD sessions are short-lived; dial again to start over",
		}

	case errors.Is(err, session.ErrSessionNotActive):
		return http.StatusConflict, errorDetail{
			Code:    CodeSessionNotActive,
			Message: "session has already ended",
			Hint:    "dial the service code again to start a new session",
		}

	case errors.Is(err, session.ErrInvalidInput):
		return http.StatusBadRequest, errorDetail{
			Code:    CodeInvalidInput,
			Message: err.Error(),
		}
	}

	// Failures originating in the developer's own application. These are
	// reported as gateway errors because the simulator is, in that moment,
	// acting as a gateway in front of their code.
	var appErr *appclient.Error
	if errors.As(err, &appErr) {
		return applicationStatus(appErr.Code), errorDetail{
			Code:    string(appErr.Code),
			Message: appErr.Message,
			Hint:    appErr.Hint,
		}
	}

	// An application failure the transport cannot classify in detail is still
	// an application failure. Reporting it as 500 would blame the simulator for
	// the developer's bug.
	if errors.Is(err, session.ErrApplicationFailure) {
		return http.StatusBadGateway, errorDetail{
			Code:    CodeApplicationFailure,
			Message: "the application did not return a usable response",
			Hint:    "check your application's logs and the callback URL in ussd.yaml",
		}
	}

	return http.StatusInternalServerError, errorDetail{
		Code:    CodeInternal,
		Message: "the simulator encountered an unexpected error",
	}
}

// applicationStatus distinguishes "your application never answered" (504) from
// "your application answered badly" (502).
func applicationStatus(code appclient.ErrorCode) int {
	if code == appclient.CodeTimeout {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// asMaxBytes is a small helper so decodeJSON can branch on a body-size error.
func asMaxBytes(err error, target **http.MaxBytesError) bool {
	return errors.As(err, target)
}
