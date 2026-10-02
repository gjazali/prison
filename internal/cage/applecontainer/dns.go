package applecontainer

import (
	"context"
	"fmt"
)

type dnsHelper struct {
	driver *Driver
}

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

func (helper dnsHelper) Describe(domain string) []string {
	return []string{
		fmt.Sprintf("runs:   sudo container system dns create %s",
			domain),
		fmt.Sprintf("writes: /etc/resolver/containerization.%s "+
			"(system-wide)", domain),
		fmt.Sprintf("undo:   sudo container system dns delete %s",
			domain),
	}
}

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
		return fmt.Errorf("cannot register the DNS domain %s", domain)
	}
	return nil
}

func (helper dnsHelper) RepairHint(domain string) []string {
	return []string{
		"if a hostname does not resolve, run:",
		fmt.Sprintf("  sudo container system dns delete %s", domain),
		fmt.Sprintf("  sudo container system dns create %s", domain),
	}
}
