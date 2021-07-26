package status

import (
	"context"
	"fmt"

	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// https://cloud.google.com/apis/design/errors

type grpcstatus interface {
	GRPCStatus() *status.Status
}

// type GStatus interface {
// 	Code() codes.Code
// 	Message() string
// 	ErrorReason() string
// 	ErrorPath() string
// 	Detail() []string
// }

// Status for rich gNMI error description
type Status struct {
	Code    codes.Code
	Message string
	Tag     ErrorTag
}

func (s *Status) Error() string {
	if s == nil {
		return ""
	}
	return fmt.Sprintf("[%s] %s", s.Tag, s.Message)
}

// GRPCStatus returns gRPC status
func (s *Status) GRPCStatus() *status.Status {
	msg := fmt.Sprintf("[%s] %s", s.Tag, s.Message)
	ss := status.New(s.Code, msg)
	return ss
}

// ToProto return gRPC error status
func ToProto(err error) *spb.Status {
	if err == nil {
		return nil
	}
	s := FromError(err)
	return s.GRPCStatus().Proto()
}

func EmptyProto() *spb.Status {
	return &spb.Status{}
}

// New returns a Status representing c and msg.
func New(c codes.Code, tag ErrorTag, msg string) *Status {
	s := &Status{
		Code:    c,
		Message: msg,
		Tag:     tag,
	}
	return s
}

// Newf returns New(c, fmt.Sprintf(format, a...)).
func Newf(c codes.Code, tag ErrorTag, format string, a ...interface{}) *Status {
	return New(c, tag, fmt.Sprintf(format, a...))
}

// Error returns an error representing c and msg.  If c is OK, returns nil.
func Error(c codes.Code, err error) error {
	s := FromError(err)
	if s == nil {
		return s
	}
	if s.Code == codes.Unknown || s.Code == codes.OK {
		s.Code = c
	}
	return s
}

// Errorf returns Error(c, fmt.Sprintf(format, a...)).
func Errorf(c codes.Code, format string, a ...interface{}) error {
	return New(c, 0, fmt.Sprintf(format, a...))
}

// ErrorTag for simple error indication
type ErrorTag int

const (
	// TagUnknown - ErrorTag
	TagUnknown ErrorTag = iota
	// TagMissingPath - invalid path
	TagMissingPath
	// TagInvalidPath - ErrorTag
	TagInvalidPath
	// TagUnknownPath - ErrorTag
	TagUnknownPath
	// TagInvalidAlias - ErrorTag
	TagInvalidAlias
	// TagDataMissing - ErrorTag
	TagDataMissing
	// TagBadData - ErrorTag
	TagBadData
	// TagNotSupport - ErrorTag
	TagNotSupport
	// TagOperationFail - ErrorTag
	TagOperationFail
	// TagMalformedMessage - ErrorTag
	TagMalformedMessage
	// TagInvalidConfig - ErrorTag
	TagInvalidConfig
	// TagInvalidSession - ErrorTag
	TagInvalidSession

	tagMax
)

var tagStr map[ErrorTag]string = map[ErrorTag]string{
	TagMissingPath:      "missing-path",
	TagInvalidPath:      "invalid-path",
	TagUnknownPath:      "unknown-path",
	TagInvalidAlias:     "invalid-path",
	TagDataMissing:      "data-missing",
	TagBadData:          "bad-data",
	TagNotSupport:       "not-support",
	TagOperationFail:    "operation-fail",
	TagMalformedMessage: "malformed-message",
	TagInvalidConfig:    "invalid-config",
	TagInvalidSession:   "invalid-session",
}

func (t ErrorTag) String() string {
	if s, ok := tagStr[t]; ok {
		return s
	}
	return "unknown-error"
}

// TaggedError returns a tagged error (netconf basis)
// Tag:
//  - invalid-path
//  - invalid-alias
//  - data-missing
//  - in-use
//  - invalid-value
//  - too-big
//  - missing-attribute
//  - bad-attribute
//  - unknown-attribute
//  - missing-element
//  - bad-element
//  - unknown-element
//  - access-denied
//  - unknown-namespace
//  - lock-denied
//  - resource-denied
//  - rollback-failed
//  - data-exists
//  - operation-not-supported
//  - operation-failed
//  - partial-operation
//  - malformed-message

func TaggedError(c codes.Code, tag ErrorTag, err error) error {
	s := FromError(err)
	if s == nil {
		return s
	}
	if s.Code == codes.Unknown || s.Code == codes.OK {
		s.Code = c
	}
	if s.Tag >= tagMax {
		s.Tag = tag
	}
	return s
}

// TaggedErrorf returns a tagged error
func TaggedErrorf(c codes.Code, tag ErrorTag, format string, a ...interface{}) error {
	return New(c, tag, fmt.Sprintf(format, a...))
}

// FromError returns a Status representing err if it was produced from this
// package or has a method `GRPCStatus() *Status`. Otherwise, ok is false and a
// Status is returned with codes.Unknown and the original error Message.
func FromError(err error) *Status {
	if err == nil {
		// s := New(codes.OK, "")
		// return s
		return nil
	}
	if s, ok := err.(*Status); ok {
		return s
	}
	if gs, ok := err.(grpcstatus); ok {
		s := New(gs.GRPCStatus().Code(), TagUnknown, gs.GRPCStatus().Message())
		return s
	}
	return New(codes.Unknown, TagUnknown, err.Error())
}

// // Convert is a convenience function which removes the need to handle the
// // boolean return value from FromError.
// func Convert(err error) *Status {
// 	s, _ := FromError(err)
// 	return s
// }

// Code returns the Code of the error if it is a Status error, codes.OK if err
// is nil, or codes.Unknown otherwise.
func Code(err error) codes.Code {
	// Don't use FromError to avoid allocation of OK status.
	if err == nil {
		return codes.OK
	}
	if s, ok := err.(*Status); ok {
		return s.Code
	}
	if gs, ok := err.(grpcstatus); ok {
		return gs.GRPCStatus().Code()
	}
	return codes.Unknown
}

// Message returns the Code of the error if it is a Status error, codes.OK if err
// is nil, or codes.Unknown otherwise.
func Message(err error) string {
	// Don't use FromError to avoid allocation of OK status.
	if err == nil {
		return ""
	}
	if s, ok := err.(*Status); ok {
		return s.Message
	}
	if gs, ok := err.(grpcstatus); ok {
		return gs.GRPCStatus().Message()
	}
	return err.Error()
}

// FromContextError converts a context error into a Status.  It returns a
// Status with codes.OK if err is nil, or a Status with codes.Unknown if err is
// non-nil and not a context error.
func FromContextError(err error) error {
	switch err {
	case nil:
		return nil
	case context.DeadlineExceeded:
		return New(codes.DeadlineExceeded, TagUnknown, err.Error())
	case context.Canceled:
		return New(codes.Canceled, TagUnknown, err.Error())
	default:
		return New(codes.Unknown, TagUnknown, err.Error())
	}
}

// UpdateResultError returns s's status as an spb.Status proto Message.
func UpdateResultError(err error) *gnmipb.Error {
	if err == nil {
		return nil
	}
	s := FromError(err)
	e := &gnmipb.Error{
		Code:    uint32(s.Code),
		Message: fmt.Sprintf("[%s] %s", s.Tag, s.Message),
	}
	return e
}
