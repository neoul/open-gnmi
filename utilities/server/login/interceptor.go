// login package supports User Authentication based on the gRPC interceptor.
// Register the following Interceptors as gRPC options before creating gRPC server.
//
//  opts = append(opts, grpc.UnaryInterceptor(login.UnaryInterceptor))
//  opts = append(opts, grpc.StreamInterceptor(login.StreamInterceptor))
//  g := grpc.NewServer(opts...)
package login

import (
	"github.com/neoul/open-gnmi/utilities"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnaryInterceptor - used to intercept the unary gRPC requests and responses
func UnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	meta, ok := utilities.GetMetadata(ctx)
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "no metadata")
	}
	username, ok := meta["username"]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "no username")
	}
	password, ok := meta["password"]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "no password")
	}
	err := AuthenticateUser(username, password)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}
	return handler(ctx, req)
}

// wrappedStream wraps around the embedded grpc.ServerStream,
// and intercepts the RecvMsg and SendMsg method call.
type wrappedStream struct {
	grpc.ServerStream
}

func (w *wrappedStream) RecvMsg(m interface{}) error {
	return w.ServerStream.RecvMsg(m)
}

func (w *wrappedStream) SendMsg(m interface{}) error {
	return w.ServerStream.SendMsg(m)
}

func newWrappedStream(s grpc.ServerStream) grpc.ServerStream {
	return &wrappedStream{s}
}

// StreamInterceptor - used to intercept the unary gRPC Streams
func StreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	meta, ok := utilities.GetMetadata(ss.Context())
	if !ok {
		return status.Errorf(codes.InvalidArgument, "no metadata")
	}
	username, ok := meta["username"]
	if !ok {
		return status.Errorf(codes.InvalidArgument, "no username")
	}
	password, ok := meta["password"]
	if !ok {
		return status.Errorf(codes.InvalidArgument, "no password")
	}
	err := AuthenticateUser(username, password)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, err.Error())
	}
	return handler(srv, newWrappedStream(ss))
}
