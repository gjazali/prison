package applecontainer

import (
	"context"

	"prison/internal/isolator"
)

const (
	AnchorName    = "prison-anchor"
	AnchorNetwork = "default"
	AnchorImage   = "docker.io/library/alpine:latest"
	AnchorMemory  = "200M" // Minimum accepted by Apple container.
	AnchorCPUs    = 1
)

type anchorHelper struct {
	driver *Driver
}

func (anchorHelper) Name() string {
	return AnchorName
}

func (anchorHelper) Network() string {
	return AnchorNetwork
}

func (helper anchorHelper) Status(ctx context.Context) (isolator.BoxInfo, error) {
	return helper.driver.Box(ctx, AnchorName)
}

func (helper anchorHelper) Ensure(ctx context.Context) error {
	info, err := helper.Status(ctx)
	if err != nil {
		return err
	}
	if info.Exists {
		if info.Network == AnchorNetwork {
			if info.Running {
				return nil
			}
			return helper.driver.Start(ctx, AnchorName)
		}
		if info.Running {
			_ = helper.driver.Stop(ctx, AnchorName)
		}
		if err := helper.driver.Delete(ctx, AnchorName); err != nil {
			return err
		}
	}
	return helper.driver.Create(ctx, isolator.CreateSpec{
		Name:    AnchorName,
		Image:   AnchorImage,
		Network: AnchorNetwork,
		CPUs:    AnchorCPUs,
		Memory:  AnchorMemory,
		Command: []string{"sleep", "infinity"},
	})
}

func (helper anchorHelper) Remove(ctx context.Context) error {
	info, err := helper.Status(ctx)
	if err != nil {
		return err
	}
	if !info.Exists {
		return nil
	}
	if info.Running {
		if err := helper.driver.Stop(ctx, AnchorName); err != nil {
			return err
		}
	}
	if err := helper.driver.Delete(ctx, AnchorName); err != nil {
		current, lookupErr := helper.Status(ctx)
		if lookupErr != nil || current.Exists {
			return err
		}
	}
	return nil
}
