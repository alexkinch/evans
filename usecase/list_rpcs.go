package usecase

import (
	"strings"

	"github.com/ktr0731/evans/grpc"
	"github.com/ktr0731/evans/proto"
	"github.com/pkg/errors"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// ListRPCs lists all RPC belong to the selected service.
// If svcName is empty, the currently selected service will be used.
// In this case, ListRPCs doesn't modify the currently selected service.
func ListRPCs(svcName string) ([]*grpc.RPC, error) {
	return dm.ListRPCs(svcName)
}
func (m *dependencyManager) ListRPCs(svcName string) ([]*grpc.RPC, error) {
	if svcName == "" {
		svcName = m.state.selectedService
	}
	fqsn := proto.FullyQualifiedServiceName(m.state.selectedPackage, svcName)
	return m.listRPCs(fqsn)
}

func (m *dependencyManager) listRPCs(fqsn string) ([]*grpc.RPC, error) {
	var rpcs []*grpc.RPC
	svcs, err := m.descSource.ListServices()
	if err != nil {
		return nil, err
	}

	var targetServices []string
	if fqsn != "" {
		// Looking for a specific service
		targetServices = []string{fqsn}
	} else {
		// Looking for all services
		targetServices = svcs
	}

	for _, service := range targetServices {
		d, err := m.descSource.FindSymbol(service)
		if err != nil {
			// Check if this is a dependency resolution error
			if strings.Contains(err.Error(), "File not found:") || strings.Contains(err.Error(), "failed to find file containing symbol") {
				if fqsn != "" {
					// If we're looking for a specific service, return a helpful error
					return nil, errors.Wrapf(err, "service %s has unresolvable dependencies (this may be due to missing proto dependencies)", service)
				} else {
					// If we're listing all services, skip this one and continue
					continue
				}
			}
			return nil, errors.Wrapf(err, "failed to resolve service %s", service)
		}

		sd := d.(protoreflect.ServiceDescriptor) // TODO: handle "ok".
		for i := 0; i < sd.Methods().Len(); i++ {
			md := sd.Methods().Get(i)
			rpcs = append(rpcs, &grpc.RPC{
				Name:               string(md.Name()),
				FullyQualifiedName: string(md.FullName()),
				RequestType: &grpc.Type{
					Name:               string(md.Input().Name()),
					FullyQualifiedName: string(md.Input().FullName()),
					New: func() interface{} {
						return dynamicpb.NewMessageType(md.Input())
					},
				},
				ResponseType: &grpc.Type{
					Name:               string(md.Output().Name()),
					FullyQualifiedName: string(md.Output().FullName()),
					New: func() interface{} {
						return dynamicpb.NewMessageType(md.Output())
					},
				},
				IsServerStreaming: md.IsStreamingServer(),
				IsClientStreaming: md.IsStreamingClient(),
			})
		}
	}

	return rpcs, nil
}
