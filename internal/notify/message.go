package notify

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Message struct {
	EventID    string
	EventType  string
	Subject    string
	Body       string
	OccurredAt time.Time
}

type Sender interface {
	Send(context.Context, Message) error
}

type Error struct {
	Code string
}

func (e Error) Error() string { return e.Code }

func stableError(code string) error { return Error{Code: code} }

func hasHeaderInjection(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func validateMessage(msg Message) error {
	if strings.TrimSpace(msg.EventType) == "" || strings.TrimSpace(msg.Subject) == "" {
		return stableError("notify_invalid_message")
	}
	if hasHeaderInjection(msg.EventID) || hasHeaderInjection(msg.EventType) || hasHeaderInjection(msg.Subject) {
		return stableError("notify_header_injection")
	}
	return nil
}

func occurredAt(msg Message) time.Time {
	if msg.OccurredAt.IsZero() {
		return time.Now().UTC()
	}
	return msg.OccurredAt.UTC()
}

func isStableError(err error, code string) bool {
	var target Error
	return errors.As(err, &target) && target.Code == code
}
