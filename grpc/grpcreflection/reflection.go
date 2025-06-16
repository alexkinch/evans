// Package grpcreflection provides gRPC reflection client.
// Currently, gRPC reflection depends on Protocol Buffers, so we split this package from grpc package.
package grpcreflection

import (
	"context"
	"strings"

	gr "github.com/jhump/protoreflect/grpcreflect"
	"github.com/ktr0731/grpc-web-go-client/grpcweb"
	"github.com/ktr0731/grpc-web-go-client/grpcweb/grpcweb_reflection_v1alpha"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// ServiceName represents the gRPC reflection service name.
const ServiceName = "grpc.reflection.v1alpha.ServerReflection"

var ErrTLSHandshakeFailed = errors.New("TLS handshake failed")

// Client defines gRPC reflection client.
type Client interface {
	// ListServices lists registered service names.
	// ListServices returns these errors:
	//   - ErrTLSHandshakeFailed: TLS misconfig.
	ListServices() ([]string, error)
	// FindSymbol returns the symbol associated with the given name.
	FindSymbol(name string) (protoreflect.Descriptor, error)
	// Reset clears internal states of Client.
	Reset()
}

type client struct {
	resolver *protoregistry.Files
	client   *gr.Client
}

func getCtx(headers map[string][]string) context.Context {
	md := metadata.New(nil)
	for k, v := range headers {
		md.Append(k, v...)
	}
	return metadata.NewOutgoingContext(context.Background(), md)
}

// NewClient returns an instance of gRPC reflection client for gRPC protocol.
func NewClient(conn grpc.ClientConnInterface, headers map[string][]string) Client {
	return &client{
		client:   gr.NewClientV1Alpha(getCtx(headers), grpc_reflection_v1alpha.NewServerReflectionClient(conn)),
		resolver: protoregistry.GlobalFiles,
	}
}

// NewWebClient returns an instance of gRPC reflection client for gRPC-Web protocol.
func NewWebClient(conn *grpcweb.ClientConn, headers map[string][]string) Client {
	return &client{
		client:   gr.NewClientV1Alpha(getCtx(headers), grpcweb_reflection_v1alpha.NewServerReflectionClient(conn)),
		resolver: protoregistry.GlobalFiles,
	}
}

func (c *client) ListServices() ([]string, error) {
	svcs, err := c.client.ListServices()
	if err != nil {
		msg := status.Convert(err).Message()
		// Check whether the error message contains TLS related error.
		// If the server didn't enable TLS, the error message contains the first string.
		// If Evans didn't enable TLS against to the TLS enabled server, the error message contains
		// the second string.
		if strings.Contains(msg, "tls: first record does not look like a TLS handshake") ||
			strings.Contains(msg, "latest connection error: <nil>") {
			return nil, ErrTLSHandshakeFailed
		}
		return nil, errors.Wrap(err, "failed to list services from reflection enabled gRPC server")
	}

	return svcs, nil
}

func (c *client) FindSymbol(name string) (protoreflect.Descriptor, error) {
	fullName := protoreflect.FullName(name)

	d, err := c.resolver.FindDescriptorByName(fullName)
	if err != nil && !errors.Is(err, protoregistry.NotFound) {
		return nil, err
	}
	if err == nil {
		return d, nil
	}

	// First try the normal approach
	jfd, err := c.client.FileContainingSymbol(name)
	if err == nil {
		// Use FileOptions with AllowUnresolvable to handle missing dependencies gracefully
		opts := protodesc.FileOptions{
			AllowUnresolvable: true,
		}
		fd, err := opts.New(jfd.AsFileDescriptorProto(), c.resolver)
		if err != nil {
			return nil, err
		}

		if err := c.resolver.RegisterFile(fd); err != nil {
			return nil, err
		}

		return c.resolver.FindDescriptorByName(fullName)
	}

	// If FileContainingSymbol fails (likely due to missing dependencies),
	// try to get all available file descriptors and find the symbol
	services, listErr := c.client.ListServices()
	if listErr != nil {
		// Return the original error if we can't even list services
		return nil, errors.Wrap(err, "failed to find file containing symbol")
	}

	// Try to find the symbol by examining each service file
	for _, serviceName := range services {
		serviceFile, serviceErr := c.client.FileContainingSymbol(serviceName)
		if serviceErr != nil {
			continue // Skip services we can't access
		}

		// Use FileOptions with AllowUnresolvable for each file
		opts := protodesc.FileOptions{
			AllowUnresolvable: true,
		}
		fd, createErr := opts.New(serviceFile.AsFileDescriptorProto(), c.resolver)
		if createErr != nil {
			continue // Skip files we can't create descriptors for
		}

		// Register the file if we haven't already
		if regErr := c.resolver.RegisterFile(fd); regErr != nil {
			// File might already be registered, that's okay
		}

		// Check if our symbol is in this file
		if d, findErr := c.resolver.FindDescriptorByName(fullName); findErr == nil {
			return d, nil
		}
	}

	// If we still can't find it, return the original error
	return nil, errors.Wrap(err, "failed to find file containing symbol")
}

func (c *client) Reset() {
	c.client.Reset()
}

// createStubFileDescriptor creates a minimal stub file descriptor for missing dependencies
func createStubFileDescriptor(fileName string) *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    &fileName,
		Package: nil, // Empty package for now
		Syntax:  stringPtr("proto3"),
	}
}

func stringPtr(s string) *string {
	return &s
}

// tolerantFileResolver wraps the standard resolver to be more tolerant of missing files
type tolerantFileResolver struct {
	*protoregistry.Files
}

func (r *tolerantFileResolver) FindFileByPath(path string) (protoreflect.FileDescriptor, error) {
	// First try the normal resolution
	fd, err := r.Files.FindFileByPath(path)
	if err == nil {
		return fd, nil
	}

	// If it's a known problematic dependency, create a stub
	if isKnownProblematicDependency(path) {
		stubProto := createStubFileDescriptor(path)
		opts := protodesc.FileOptions{
			AllowUnresolvable: true,
		}
		return opts.New(stubProto, r.Files)
	}

	return nil, err
}

func isKnownProblematicDependency(path string) bool {
	knownProblematic := []string{
		"gorm/options.proto",
		"google/protobuf/descriptor.proto",
		"validate/validate.proto",
	}
	for _, known := range knownProblematic {
		if strings.Contains(path, known) {
			return true
		}
	}
	return false
}
