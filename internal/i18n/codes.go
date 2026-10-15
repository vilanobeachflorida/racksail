package i18n

import "net/http"

// Core error codes. Modules declare their own codes next to the code that
// returns them.
var (
	ErrInternal         = Define("core.internal", http.StatusInternalServerError, "Something went wrong on the server. The details are in the server log.")
	ErrBadRequest       = Define("core.bad_request", http.StatusBadRequest, "The request couldn't be read: {reason}.")
	ErrInvalid          = Define("core.invalid", http.StatusUnprocessableEntity, "Some values aren't valid. Fix the highlighted fields and try again.")
	ErrNotFound         = Define("core.not_found", http.StatusNotFound, "{what} doesn't exist or was deleted.")
	ErrMethodNotAllowed = Define("core.method_not_allowed", http.StatusMethodNotAllowed, "{path} doesn't accept {method}; use {allowed}.")
	ErrConflict         = Define("core.conflict", http.StatusConflict, "{what} was changed by someone else. Reload it and try again.")
	ErrAlreadyExists    = Define("core.already_exists", http.StatusConflict, "{what} already exists.")
	ErrUnauthenticated  = Define("core.unauthenticated", http.StatusUnauthorized, "Sign in to continue.")
	ErrForbidden        = Define("core.forbidden", http.StatusForbidden, "You don't have permission to {action}.")
	ErrRateLimited      = Define("core.rate_limited", http.StatusTooManyRequests, "Too many requests. Wait {retryAfter} seconds and try again.")
	ErrUnavailable      = Define("core.unavailable", http.StatusServiceUnavailable, "{what} isn't available right now. Try again in a moment.")
	ErrTimeout          = Define("core.timeout", http.StatusGatewayTimeout, "{what} took too long and was stopped.")
	ErrCanceled         = Define("core.canceled", http.StatusConflict, "The operation was canceled before it finished.")
	ErrTooLarge         = Define("core.too_large", http.StatusRequestEntityTooLarge, "The request is larger than the {limit} limit.")
	ErrUnsupported      = Define("core.unsupported", http.StatusUnprocessableEntity, "{what} isn't supported on this server: {reason}.")
)

// Field-level codes used inside core.invalid errors.
var (
	ErrRequired    = Define("core.required", http.StatusUnprocessableEntity, "This field is required.")
	ErrBadValue    = Define("core.bad_value", http.StatusUnprocessableEntity, "This value isn't valid: {reason}.")
	ErrTooLong     = Define("core.too_long", http.StatusUnprocessableEntity, "Use at most {max} characters.")
	ErrOutOfRange  = Define("core.out_of_range", http.StatusUnprocessableEntity, "Use a value from {min} to {max}.")
	ErrImmutable   = Define("core.immutable", http.StatusUnprocessableEntity, "This field can't be changed after it's created.")
	ErrUnknownName = Define("core.unknown_name", http.StatusUnprocessableEntity, "There is no {what} called {name}.")
)
