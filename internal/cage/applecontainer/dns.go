package applecontainer

import (
	"context"
	"fmt"
)

// dnsHelper registers local DNS domains with the backend's resolver.
type dnsHelper struct {
	driver *Driver
}

// Exists returns true if the domain is already registered.
func (helper dnsHelper) Exists(
	ctx context.Context, domain string,
) (bool, error) {
	var domains []string
	found, err := helper.driver.containerJSON(
		ctx, &domains, "system", "dns", "list", "--format", "json")
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	for _, registered := range domains {
		if registered == domain {
			return true, nil
		}
	}
	return false, nil
}

// Describe returns lines explaining what registering the domain does
// and how to undo it.
func (helper dnsHelper) Describe(domain string) []string {
	return []string{
		fmt.Sprintf("runs:   sudo container system dns create %s",
			domain),
		fmt.Sprintf("writes: /etc/resolver/containerization.%s, "+
			"a system-wide change", domain),
		fmt.Sprintf("undo:   sudo container system dns delete %s",
			domain),
	}
}

// Register registers the domain via sudo.
func (helper dnsHelper) Register(
	ctx context.Context, domain string,
) error {
	status, err := helper.driver.runner.Run(ctx, Command{
		Name: "sudo",
		Arguments: []string{
			containerBinary, "system", "dns", "create", domain,
		},
		InheritStreams: true,
	})
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("registering the DNS domain %s failed",
			domain)
	}
	return nil
}

// RepairHint returns lines to print when a registered domain stops
// resolving.
func (helper dnsHelper) RepairHint(domain string) []string {
	return []string{
		"if a hostname stops resolving, recreate it:",
		fmt.Sprintf("  sudo container system dns delete %s", domain),
		fmt.Sprintf("  sudo container system dns create %s", domain),
	}
}
