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
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
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
	// GetAllMessages extracts all message types from all available services without full dependency resolution
	GetAllMessages() ([]string, error)
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
	ctx := getCtx(headers)
	// Use the same approach as grpcurl: NewClientAuto + AllowMissingFileDescriptors
	reflectionClient := gr.NewClientAuto(ctx, conn)
	reflectionClient.AllowMissingFileDescriptors() // This is the key difference!

	return &client{
		client:   reflectionClient,
		resolver: protoregistry.GlobalFiles,
	}
}

// NewWebClient returns an instance of gRPC reflection client for gRPC-Web protocol.
func NewWebClient(conn *grpcweb.ClientConn, headers map[string][]string) Client {
	ctx := getCtx(headers)
	// Use the same pattern as the regular client
	reflectionClient := gr.NewClientV1Alpha(ctx, grpcweb_reflection_v1alpha.NewServerReflectionClient(conn))
	reflectionClient.AllowMissingFileDescriptors() // Apply the same fix for web client

	return &client{
		client:   reflectionClient,
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

	// Get file from reflection - this should work now with AllowMissingFileDescriptors()
	jfd, err := c.client.FileContainingSymbol(name)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find file containing symbol")
	}

	// Convert from jhump/protoreflect descriptor to protoreflect.Descriptor
	opts := protodesc.FileOptions{
		AllowUnresolvable: true,
	}
	fd, err := opts.New(jfd.AsFileDescriptorProto(), c.resolver)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create file descriptor")
	}

	if err := c.resolver.RegisterFile(fd); err != nil {
		return nil, errors.Wrap(err, "failed to register file descriptor")
	}

	return c.resolver.FindDescriptorByName(fullName)
}

func (c *client) Reset() {
	c.client.Reset()
}

// GetAllMessages extracts all message types from all available services without full dependency resolution
func (c *client) GetAllMessages() ([]string, error) {
	var messages []string
	messageSet := make(map[string]bool)

	services, err := c.client.ListServices()
	if err != nil {
		return nil, err
	}

	for _, serviceName := range services {
		serviceFile, serviceErr := c.client.FileContainingSymbol(serviceName)
		if serviceErr != nil {
			continue // Skip services we can't access
		}

		fileProto := serviceFile.AsFileDescriptorProto()
		pkg := ""
		if fileProto.Package != nil {
			pkg = *fileProto.Package
		}

		// Extract message types directly from the proto
		for _, msgType := range fileProto.MessageType {
			if msgType.Name == nil {
				continue
			}

			var msgName string
			if pkg != "" {
				msgName = pkg + "." + *msgType.Name
			} else {
				msgName = *msgType.Name
			}

			if !messageSet[msgName] {
				messages = append(messages, msgName)
				messageSet[msgName] = true
			}
		}

		// Also extract message types from services (request/response types)
		for _, service := range fileProto.Service {
			if service.Method == nil {
				continue
			}
			for _, method := range service.Method {
				if method.InputType != nil {
					inputType := strings.TrimPrefix(*method.InputType, ".")
					if !messageSet[inputType] {
						messages = append(messages, inputType)
						messageSet[inputType] = true
					}
				}
				if method.OutputType != nil {
					outputType := strings.TrimPrefix(*method.OutputType, ".")
					if !messageSet[outputType] {
						messages = append(messages, outputType)
						messageSet[outputType] = true
					}
				}
			}
		}
	}

	return messages, nil
}
