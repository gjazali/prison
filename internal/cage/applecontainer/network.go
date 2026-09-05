package applecontainer

import (
	"context"
	"fmt"

	"prison/internal/cage"
)

// networkDocument is the JSON structure from network inspect and list
// commands, trimmed to needed fields.
type networkDocument struct {
	Configuration struct {
		Mode string `json:"mode"`
		Name string `json:"name"`
	} `json:"configuration"`
	Status struct {
		IPv4Gateway string `json:"ipv4Gateway"`
		IPv4Subnet  string `json:"ipv4Subnet"`
		IPv6Subnet  string `json:"ipv6Subnet"`
	} `json:"status"`
}

// networkInfo converts the document into a cage.NetworkInfo.
func (document networkDocument) networkInfo(name string) cage.NetworkInfo {
	info := cage.NetworkInfo{
		Name:     document.Configuration.Name,
		Exists:   true,
		HostOnly: document.Configuration.Mode == "hostOnly",
		Gateway:  document.Status.IPv4Gateway,
		SubnetV4: document.Status.IPv4Subnet,
		SubnetV6: document.Status.IPv6Subnet,
	}
	if info.Name == "" {
		info.Name = name
	}
	return info
}

// Network returns info about a network by name. Returns Exists false
// for unknown names.
func (driver *Driver) Network(
	ctx context.Context, name string,
) (cage.NetworkInfo, error) {
	var documents []networkDocument
	found, err := driver.containerJSON(
		ctx, &documents, "network", "inspect", name)
	if err != nil {
		return cage.NetworkInfo{}, err
	}
	if !found || len(documents) == 0 {
		return cage.NetworkInfo{Name: name}, nil
	}
	return documents[0].networkInfo(name), nil
}

// EnsureNetwork creates a host-only network if it does not exist.
// Returns an error if the existing network is not host-only.
func (driver *Driver) EnsureNetwork(
	ctx context.Context, name string,
) error {
	existing, err := driver.Network(ctx, name)
	if err != nil {
		return err
	}
	if existing.Exists {
		if !existing.HostOnly {
			return fmt.Errorf(
				"network %s is not host-only; delete it with "+
					"`container network delete %s` and let prison "+
					"create it again", name, name)
		}
		return nil
	}
	_, stderr, status, err := driver.runCapturing(
		ctx, containerBinary,
		"network", "create", "--internal", name)
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("creating network %s failed%s",
			name, trailingDetail(stderr))
	}
	created, err := driver.Network(ctx, name)
	if err != nil {
		return err
	}
	if !created.Exists || !created.HostOnly {
		return fmt.Errorf(
			"network %s was created but is not host-only", name)
	}
	return nil
}
