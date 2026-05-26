package callbacks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrorClass represents the category of an error for retry/escalation decisions.
type ErrorClass string

const (
	ErrorClassTransient ErrorClass = "transient"
	ErrorClassPermanent ErrorClass = "permanent"
	ErrorClassTimeout   ErrorClass = "timeout"
	ErrorClassUnknown   ErrorClass = "unknown"
)

// ClassifiedError wraps an error with its classification and metadata.
type ClassifiedError struct {
	Original     error
	Class        ErrorClass
	Component    string
	RunID        string
	ClassifiedAt time.Time
	Retryable    bool
}

func (ce *ClassifiedError) Error() string {
	return fmt.Sprintf("[%s] %s: %v", ce.Class, ce.Component, ce.Original)
}

func (ce *ClassifiedError) Unwrap() error { return ce.Original }

type classifiedErrorKey struct{}

// WithClassifiedError stores the latest ClassifiedError in the context.
func WithClassifiedError(ctx context.Context, ce *ClassifiedError) context.Context {
	return context.WithValue(ctx, classifiedErrorKey{}, ce)
}

// ClassifiedErrorFromContext retrieves the ClassifiedError from the context, or nil.
func ClassifiedErrorFromContext(ctx context.Context) *ClassifiedError {
	v, _ := ctx.Value(classifiedErrorKey{}).(*ClassifiedError)
	return v
}

// ClassifyError determines the ErrorClass for a given error by inspecting
// well-known error types and message patterns.
func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ErrorClassUnknown
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrorClassTimeout
	}

	msg := strings.ToLower(err.Error())

	for _, pattern := range []string{
		"connection refused", "connection reset",
		"temporary failure", "service unavailable",
		"too many requests", "rate limit",
		"i/o timeout", "dial tcp",
	} {
		if strings.Contains(msg, pattern) {
			return ErrorClassTransient
		}
	}

	for _, pattern := range []string{
		"not found", "permission denied", "unauthorized",
		"invalid argument", "validation failed",
		"schema mismatch", "unsupported",
	} {
		if strings.Contains(msg, pattern) {
			return ErrorClassPermanent
		}
	}

	return ErrorClassUnknown
}

// ErrorClassifyingHandler wraps a Handler and enriches the context with
// ClassifiedError data on OnError calls. The inner handler receives the
// enriched context.
type ErrorClassifyingHandler struct {
	Inner Handler
}

func (h *ErrorClassifyingHandler) OnStart(ctx context.Context, info *RunInfo, input any) context.Context {
	if h.Inner != nil {
		return h.Inner.OnStart(ctx, info, input)
	}
	return ctx
}

func (h *ErrorClassifyingHandler) OnEnd(ctx context.Context, info *RunInfo, output any) context.Context {
	if h.Inner != nil {
		return h.Inner.OnEnd(ctx, info, output)
	}
	return ctx
}

func (h *ErrorClassifyingHandler) OnError(ctx context.Context, info *RunInfo, err error) context.Context {
	class := ClassifyError(err)
	ce := &ClassifiedError{
		Original:     err,
		Class:        class,
		Component:    info.ComponentName,
		RunID:        info.RunID,
		ClassifiedAt: time.Now(),
		Retryable:    class == ErrorClassTransient || class == ErrorClassTimeout,
	}
	ctx = WithClassifiedError(ctx, ce)
	if h.Inner != nil {
		return h.Inner.OnError(ctx, info, err)
	}
	return ctx
}
