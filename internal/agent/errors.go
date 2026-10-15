package agent

import (
	"errors"
	"net/http"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Error codes the agent returns.
var (
	ErrUnknownOperation = i18n.Define("agent.unknown_operation", http.StatusNotFound, "The agent has no operation called {op}.")
	ErrInvalidInput     = i18n.Define("agent.invalid_input", http.StatusUnprocessableEntity, "The {op} operation was given invalid input: {reason}.")
	ErrNotAllowed       = i18n.Define("agent.not_allowed", http.StatusForbidden, "The {op} operation isn't allowed to touch {target}.")
	ErrFailed           = i18n.Define("agent.operation_failed", http.StatusInternalServerError, "The {op} operation failed: {reason}.")
	ErrNotFound         = i18n.Define("agent.not_found", http.StatusNotFound, "The {op} operation couldn't find what it needs: {reason}.")
	ErrConflict         = i18n.Define("agent.conflict", http.StatusConflict, "The {op} operation conflicts with the machine's state: {reason}.")
	ErrUnreachable      = i18n.Define("agent.unreachable", http.StatusServiceUnavailable, "The agent isn't reachable: {reason}.")
)

// errorCode returns the code an error carries, or "".
func errorCode(err error) string {
	var coded *i18n.Error
	if errors.As(err, &coded) {
		return string(coded.Code)
	}
	return ""
}

// failed wraps an adapter error with the operation's name and a code that
// matches what went wrong. Errors that already carry a code keep it.
func failed(op string, err error) error {
	var coded *i18n.Error
	if errors.As(err, &coded) {
		return err
	}
	switch {
	case errors.Is(err, system.ErrInvalid):
		return ErrInvalidInput.Wrap(err, "op", op, "reason", err.Error())
	case errors.Is(err, system.ErrNotFound):
		return ErrNotFound.Wrap(err, "op", op, "reason", err.Error())
	case errors.Is(err, system.ErrExists), errors.Is(err, system.ErrNotEmpty):
		return ErrConflict.Wrap(err, "op", op, "reason", err.Error())
	}
	return ErrFailed.Wrap(err, "op", op, "reason", err.Error())
}
