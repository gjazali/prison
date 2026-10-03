package applecontainer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"prison/internal/isolator"
)

const logTailLines = 20

type boxDocument struct {
	ID            string `json:"id"`
	Configuration struct {
		Image struct {
			Reference string `json:"reference"`
		} `json:"image"`
		Networks []struct {
			Network string `json:"network"`
		} `json:"networks"`
	} `json:"configuration"`
	Status struct {
		State    string `json:"state"`
		Networks []struct {
			Network     string `json:"network"`
			IPv4Address string `json:"ipv4Address"`
			IPv4Gateway string `json:"ipv4Gateway"`
		} `json:"networks"`
	} `json:"status"`
}

func (document boxDocument) boxInfo() isolator.BoxInfo {
	info := isolator.BoxInfo{
		Name:    document.ID,
		Exists:  true,
		Running: document.Status.State == "running",
		Image:   document.Configuration.Image.Reference,
	}
	if len(document.Configuration.Networks) > 0 {
		info.Network = document.Configuration.Networks[0].Network
	}
	if len(document.Status.Networks) > 0 {
		attachment := document.Status.Networks[0]
		if attachment.Network != "" {
			info.Network = attachment.Network
		}
		info.Address = strings.SplitN(attachment.IPv4Address, "/", 2)[0]
	}
	return info
}

func (driver *Driver) Box(
	ctx context.Context, name string,
) (isolator.BoxInfo, error) {
	var documents []boxDocument
	found, err := driver.containerJSON(
		ctx, &documents, "inspect", name)
	if err != nil {
		return isolator.BoxInfo{}, err
	}
	if !found || len(documents) == 0 {
		return isolator.BoxInfo{Name: name}, nil
	}
	return documents[0].boxInfo(), nil
}

func (driver *Driver) ListBoxes(
	ctx context.Context,
) ([]isolator.BoxInfo, error) {
	var documents []boxDocument
	found, err := driver.containerJSON(
		ctx, &documents, "ls", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf(
			"cannot list boxes. Run `container system start`")
	}
	boxes := make([]isolator.BoxInfo, 0, len(documents))
	for _, document := range documents {
		boxes = append(boxes, document.boxInfo())
	}
	sort.Slice(boxes, func(first, second int) bool {
		return boxes[first].Name < boxes[second].Name
	})
	return boxes, nil
}

func createArguments(spec isolator.CreateSpec) []string {
	arguments := []string{
		"run", "--detach", "--name", spec.Name,
		"--network", spec.Network,
	}
	if spec.CPUs > 0 {
		arguments = append(arguments,
			"--cpus", strconv.Itoa(spec.CPUs))
	}
	if spec.Memory != "" {
		arguments = append(arguments, "--memory", spec.Memory)
	}
	for _, capability := range spec.Capabilities {
		if !strings.HasPrefix(capability, "CAP_") {
			capability = "CAP_" + capability
		}
		arguments = append(arguments, "--cap-add", capability)
	}
	for _, mount := range spec.Mounts {
		if mount.ReadOnly {
			arguments = append(arguments, "--mount", fmt.Sprintf(
				"type=bind,source=%s,target=%s,readonly",
				mount.Source, mount.Target))
			continue
		}
		arguments = append(arguments, "--volume",
			mount.Source+":"+mount.Target)
	}
	for _, port := range spec.Ports {
		arguments = append(arguments, "--publish", fmt.Sprintf(
			"127.0.0.1:%d:%d", port.Host, port.Guest))
	}
	for _, assignment := range spec.Environment {
		arguments = append(arguments, "--env", assignment)
	}
	arguments = append(arguments, spec.Image)
	return append(arguments, spec.Command...)
}

func (driver *Driver) Create(
	ctx context.Context, spec isolator.CreateSpec,
) error {
	_, stderr, status, err := driver.runCapturing(
		ctx, containerBinary, createArguments(spec)...)
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("cannot create box %s%s",
			spec.Name, trailingDetail(stderr))
	}
	return driver.waitUntilExecutable(ctx, spec.Name)
}

func (driver *Driver) Start(ctx context.Context, name string) error {
	_, stderr, status, err := driver.runCapturing(
		ctx, containerBinary, "start", name)
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("cannot start box %s%s",
			name, trailingDetail(stderr))
	}
	return driver.waitUntilExecutable(ctx, name)
}

func (driver *Driver) Stop(ctx context.Context, name string) error {
	_, stderr, status, err := driver.runCapturing(
		ctx, containerBinary, "stop", name)
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("cannot stop box %s%s",
			name, trailingDetail(stderr))
	}
	return nil
}

func (driver *Driver) Delete(ctx context.Context, name string) error {
	deleted, err := driver.runQuietly(
		ctx, containerBinary, "delete", name)
	if err != nil {
		return err
	}
	if deleted {
		return nil
	}
	_, stderr, status, err := driver.runCapturing(
		ctx, containerBinary, "rm", name)
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("cannot delete box %s%s",
			name, trailingDetail(stderr))
	}
	return nil
}

func (driver *Driver) Logs(
	ctx context.Context, name string, w io.Writer,
) error {
	status, err := driver.runner.Run(ctx, Command{
		Name:      containerBinary,
		Arguments: []string{"logs", name},
		Stdout:    w,
		Stderr:    w,
	})
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("cannot read the logs of box %s", name)
	}
	return nil
}

func execArguments(spec isolator.ExecSpec) []string {
	arguments := []string{"exec"}
	if spec.TTY {
		arguments = append(arguments, "--tty", "--interactive")
	} else if spec.Interactive {
		arguments = append(arguments, "--interactive")
	}
	if spec.WorkDir != "" {
		arguments = append(arguments, "--workdir", spec.WorkDir)
	}
	arguments = append(arguments,
		"--uid", strconv.Itoa(spec.UID),
		"--gid", strconv.Itoa(spec.GID))
	for _, assignment := range spec.Environment {
		arguments = append(arguments, "--env", assignment)
	}
	arguments = append(arguments, spec.Box)
	return append(arguments, spec.Command...)
}

func (driver *Driver) Exec(
	ctx context.Context, spec isolator.ExecSpec,
) (int, error) {
	return driver.runner.Run(ctx, Command{
		Name:           containerBinary,
		Arguments:      execArguments(spec),
		Stdin:          spec.Stdin,
		Stdout:         spec.Stdout,
		Stderr:         spec.Stderr,
		InheritStreams: true,
	})
}

func (driver *Driver) waitUntilExecutable(
	ctx context.Context, name string,
) error {
	deadline := time.Now().Add(driver.readinessTimeout)
	for {
		ready, err := driver.runQuietly(
			ctx, containerBinary, "exec", name, "true")
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		info, err := driver.Box(ctx, name)
		if err != nil {
			return err
		}
		if !info.Exists || !info.Running {
			return fmt.Errorf("box %s stopped during startup%s",
				name, driver.logTail(ctx, name))
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("box %s did not start within %s%s",
				name, driver.readinessTimeout,
				driver.logTail(ctx, name))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(driver.readinessInterval):
		}
	}
}

func (driver *Driver) logTail(ctx context.Context, name string) string {
	var collected bytes.Buffer
	if err := driver.Logs(ctx, name, &collected); err != nil &&
		collected.Len() == 0 {
		return ""
	}
	lines := strings.Split(
		strings.TrimRight(collected.String(), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	if len(lines) > logTailLines {
		lines = lines[len(lines)-logTailLines:]
	}
	return ":\n  " + strings.Join(lines, "\n  ")
}
