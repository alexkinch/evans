package usecase

import (
	"sort"
	"strings"

	"github.com/pkg/errors"
)

// FormatMessages formats all package names.
func FormatMessages() (string, error) {
	return dm.FormatMessages()
}
func (m *dependencyManager) FormatMessages() (string, error) {
	svcs, err := m.ListServices()
	if err != nil {
		return "", err
	}

	type message struct {
		Message string `json:"message"`
	}
	var v struct {
		Messages []message `json:"messages"`
	}
	encountered := make(map[string]struct{})

	// Try the normal approach first
	normalSuccess := false
	for _, svc := range svcs {
		rpcs, err := m.ListRPCs(svc)
		if err != nil {
			// Check if this is a dependency resolution error
			if strings.Contains(err.Error(), "unresolvable dependencies") ||
				strings.Contains(err.Error(), "File not found:") ||
				strings.Contains(err.Error(), "failed to find file containing symbol") ||
				strings.Contains(err.Error(), "could not resolve import") ||
				strings.Contains(err.Error(), "proto:") {
				// Skip this service and continue with others
				continue
			}
			return "", errors.Wrap(err, "failed to list RPCs")
		}
		normalSuccess = true
		for _, rpc := range rpcs {
			if _, found := encountered[rpc.RequestType.Name]; !found {
				v.Messages = append(v.Messages, message{rpc.RequestType.Name})
				encountered[rpc.RequestType.Name] = struct{}{}
			}
			if _, found := encountered[rpc.ResponseType.Name]; !found {
				v.Messages = append(v.Messages, message{rpc.ResponseType.Name})
				encountered[rpc.ResponseType.Name] = struct{}{}
			}
		}
	}

	// If normal approach failed to get any messages, try the fallback approach
	if !normalSuccess || len(v.Messages) == 0 {
		// Use the new fallback method to extract messages directly
		if fallbackClient, ok := m.descSource.(interface{ GetAllMessages() ([]string, error) }); ok {
			messageNames, fallbackErr := fallbackClient.GetAllMessages()
			if fallbackErr == nil {
				for _, name := range messageNames {
					if _, found := encountered[name]; !found {
						v.Messages = append(v.Messages, message{name})
						encountered[name] = struct{}{}
					}
				}
			}
		}
	}

	sort.Slice(v.Messages, func(i, j int) bool {
		return v.Messages[i].Message < v.Messages[j].Message
	})
	out, err := m.resourcePresenter.Format(v)
	if err != nil {
		return "", errors.Wrap(err, "failed to format message names by presenter")
	}
	return out, nil
}
