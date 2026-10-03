package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"prison/internal/isolator"
	"prison/internal/machine"
	"prison/internal/nfsexport"
)

const (
	boxRecordFile     = "box.json"
	boxDiskFile       = "rootfs.ext4"
	configDiskFile    = "config.disk"
	firecrackerConfig = "firecracker.json"
	apiSocketFile     = "api.sock"
	vsockSocketFile   = "vsock.sock"
	consoleLogFile    = "console.log"
	processFile       = "firecracker.pid"
	guestCID          = 3
	defaultCPUs       = 2
	defaultMemoryMiB  = 2048
	unixSocketLimit   = 107
	logTailLines      = 20
	guestInitPath     = "/usr/local/bin/prison-guest"
)

type boxRecord struct {
	Name      string `json:"name"`
	Image     string `json:"image"`
	Network   string `json:"network"`
	TapIndex  int    `json:"tap_index"`
	CPUs      int    `json:"cpus"`
	MemoryMiB int    `json:"memory_mib"`

	Mounts []machine.Mount `json:"mounts,omitempty"`
}

func (driver *Driver) boxesDirectory() string {
	return filepath.Join(driver.stateDirectory, "boxes")
}

func (driver *Driver) boxDirectory(name string) string {
	return filepath.Join(driver.boxesDirectory(), name)
}

func (driver *Driver) readBoxRecord(name string) (boxRecord, bool, error) {
	var record boxRecord
	content, err := os.ReadFile(
		filepath.Join(driver.boxDirectory(name), boxRecordFile))
	if errors.Is(err, os.ErrNotExist) {
		return record, false, nil
	}
	if err != nil {
		return record, false, fmt.Errorf("cannot read box %s: %w", name, err)
	}
	if err := json.Unmarshal(content, &record); err != nil {
		return record, false, fmt.Errorf("the record of box %s is not valid: %w",
			name, err)
	}
	return record, true, nil
}

func (driver *Driver) Box(
	ctx context.Context, name string,
) (isolator.BoxInfo, error) {
	if err := driver.requireStateDirectory(); err != nil {
		return isolator.BoxInfo{}, err
	}
	record, found, err := driver.readBoxRecord(name)
	if err != nil || !found {
		return isolator.BoxInfo{Name: name}, err
	}
	return driver.boxInfo(record), nil
}

func (driver *Driver) boxInfo(record boxRecord) isolator.BoxInfo {
	info := isolator.BoxInfo{
		Name:    record.Name,
		Exists:  true,
		Running: driver.processOf(record.Name) != 0,
		Network: record.Network,
		Image:   record.Image,
	}
	if info.Running {
		if network, err := networkFor(record.Network); err == nil {
			info.Address = network.boxAddress(record.TapIndex).String()
		}
	}
	return info
}

func (driver *Driver) ListBoxes(
	ctx context.Context) ([]isolator.BoxInfo, error) {
	if err := driver.requireStateDirectory(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(driver.boxesDirectory())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot list boxes: %w", err)
	}
	var boxes []isolator.BoxInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, found, err := driver.readBoxRecord(entry.Name())
		if err != nil {
			return nil, err
		}
		if found {
			boxes = append(boxes, driver.boxInfo(record))
		}
	}
	sort.Slice(boxes, func(first, second int) bool {
		return boxes[first].Name < boxes[second].Name
	})
	return boxes, nil
}

func (driver *Driver) Create(
	ctx context.Context, spec isolator.CreateSpec) error {
	if err := driver.requireStateDirectory(); err != nil {
		return err
	}
	network, err := networkFor(spec.Network)
	if err != nil {
		return err
	}
	directory := driver.boxDirectory(spec.Name)
	socket := filepath.Join(directory, vsockSocketFile)
	if len(socket) > unixSocketLimit {
		return fmt.Errorf("the box path %s is too long for a socket. "+
			"Use a shorter `PRISON_ROOT`", directory)
	}
	disk, err := driver.ensureDisk(ctx, spec.Image, os.Stderr)
	if err != nil {
		return err
	}
	memory, err := memoryInMiB(spec.Memory)
	if err != nil {
		return err
	}
	record := boxRecord{
		Name:      spec.Name,
		Image:     spec.Image,
		Network:   spec.Network,
		CPUs:      spec.CPUs,
		MemoryMiB: memory,
	}
	if record.CPUs <= 0 {
		record.CPUs = defaultCPUs
	}
	for _, mount := range spec.Mounts {
		record.Mounts = append(record.Mounts, machine.Mount{
			Source:   mount.Source,
			Target:   mount.Target,
			ReadOnly: mount.ReadOnly,
		})
	}
	unlock, err := driver.lockBoxes()
	if err != nil {
		return err
	}
	if _, found, _ := driver.readBoxRecord(spec.Name); found {
		unlock()
		return fmt.Errorf("box %s already exists", spec.Name)
	}
	record.TapIndex, err = driver.freeTapIndex(network)
	if err == nil {
		err = driver.writeBox(ctx, record, spec, disk)
	}
	unlock()
	if err != nil {
		os.RemoveAll(directory)
		return err
	}
	return driver.boot(ctx, record)
}

func (driver *Driver) writeBox(ctx context.Context, record boxRecord,
	spec isolator.CreateSpec, disk string) error {
	directory := driver.boxDirectory(record.Name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %w", directory, err)
	}
	content, _ := json.MarshalIndent(record, "", "  ")
	if err := os.WriteFile(filepath.Join(directory, boxRecordFile),
		content, 0o600); err != nil {
		return fmt.Errorf("cannot write the record of box %s: %w",
			record.Name, err)
	}
	image, err := readImageConfig(driver.layoutPath(spec.Image))
	if err != nil {
		return err
	}
	network, err := networkFor(record.Network)
	if err != nil {
		return err
	}
	configDisk, err := machine.EncodeConfig(machine.Config{
		Hostname:    spec.Name,
		Environment: append(image.Environment, spec.Environment...),
		Command:     spec.Command,
		NFSServer:   network.gateway.String(),
		Mounts:      record.Mounts,
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, configDiskFile),
		configDisk, 0o600); err != nil {
		return fmt.Errorf("cannot write the configuration of box %s: %w",
			record.Name, err)
	}
	status, err := driver.run(ctx, hostCommand{
		name: "cp",
		arguments: []string{"--sparse=always", "--reflink=auto", disk,
			filepath.Join(directory, boxDiskFile)},
		stderr: os.Stderr,
	})
	if err != nil || status != 0 {
		return fmt.Errorf("cannot copy the disk of box %s", record.Name)
	}
	return nil
}

// lockBoxes serializes the choice of taps between prison processes.
func (driver *Driver) lockBoxes() (func(), error) {
	if err := os.MkdirAll(driver.boxesDirectory(), 0o755); err != nil {
		return nil, fmt.Errorf("cannot create %s: %w",
			driver.boxesDirectory(), err)
	}
	path := filepath.Join(driver.boxesDirectory(), ".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s: %w", path, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		file.Close()
		return nil, fmt.Errorf("cannot lock %s: %w", path, err)
	}
	return func() { file.Close() }, nil
}

func (driver *Driver) freeTapIndex(network boxNetwork) (int, error) {
	boxes, err := driver.ListBoxes(context.Background())
	if err != nil {
		return 0, err
	}
	used := map[int]bool{}
	for _, box := range boxes {
		record, found, err := driver.readBoxRecord(box.Name)
		if err == nil && found && record.Network == network.name {
			used[record.TapIndex] = true
		}
	}
	for index := range tapPoolSize {
		if !used[index] {
			return index, nil
		}
	}
	return 0, fmt.Errorf("the %s network has no free tap. Remove a box "+
		"with `prison rm`", network.name)
}

func (driver *Driver) Start(ctx context.Context, name string) error {
	record, found, err := driver.readBoxRecord(name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("box %s does not exist", name)
	}
	if driver.processOf(name) != 0 {
		return nil
	}
	return driver.boot(ctx, record)
}

// boot starts Firecracker in its own session so that it keeps running after
// prison exits.
func (driver *Driver) boot(ctx context.Context, record boxRecord) error {
	kernel, err := driver.kernelPath()
	if err != nil {
		return err
	}
	network, err := networkFor(record.Network)
	if err != nil {
		return err
	}
	if err := driver.exportMounts(ctx, record, network); err != nil {
		return err
	}
	directory := driver.boxDirectory(record.Name)
	for _, stale := range []string{apiSocketFile, vsockSocketFile} {
		os.Remove(filepath.Join(directory, stale))
	}
	configuration := firecrackerConfiguration(record, network, kernel,
		directory)
	content, _ := json.MarshalIndent(configuration, "", "  ")
	configPath := filepath.Join(directory, firecrackerConfig)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		return fmt.Errorf("cannot write %s: %w", configPath, err)
	}
	console, err := os.Create(filepath.Join(directory, consoleLogFile))
	if err != nil {
		return fmt.Errorf("cannot create the console log: %w", err)
	}
	defer console.Close()
	process := exec.Command(firecrackerBinary,
		"--api-sock", filepath.Join(directory, apiSocketFile),
		"--config-file", configPath)
	process.Stdout = console
	process.Stderr = console
	process.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := process.Start(); err != nil {
		return fmt.Errorf("cannot start firecracker: %w", err)
	}
	pid := process.Process.Pid
	if err := os.WriteFile(filepath.Join(directory, processFile),
		[]byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		process.Process.Kill()
		return fmt.Errorf("cannot record the firecracker process: %w", err)
	}
	exited := make(chan struct{})
	go func() {
		process.Wait()
		close(exited)
	}()
	return driver.waitForAgent(ctx, record.Name, exited)
}

func (driver *Driver) waitForAgent(ctx context.Context, name string,
	exited <-chan struct{}) error {
	socket := filepath.Join(driver.boxDirectory(name), vsockSocketFile)
	deadline := time.Now().Add(driver.readinessTimeout)
	for {
		attempt, cancel := context.WithTimeout(ctx, time.Second)
		err := askAgent(attempt, socket, machine.OperationPing)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-exited:
			return fmt.Errorf("box %s stopped during startup%s", name,
				driver.logTail(name))
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(driver.readinessInterval):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("box %s did not start within %s%s", name,
				driver.readinessTimeout, driver.logTail(name))
		}
	}
}

type firecrackerDrive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type firecrackerInterface struct {
	InterfaceID string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
	GuestMAC    string `json:"guest_mac"`
}

type firecrackerDocument struct {
	BootSource struct {
		KernelImagePath string `json:"kernel_image_path"`
		BootArguments   string `json:"boot_args"`
	} `json:"boot-source"`
	Drives        []firecrackerDrive `json:"drives"`
	MachineConfig struct {
		VCPUCount  int `json:"vcpu_count"`
		MemSizeMiB int `json:"mem_size_mib"`
	} `json:"machine-config"`
	NetworkInterfaces []firecrackerInterface `json:"network-interfaces"`
	Vsock             struct {
		GuestCID int    `json:"guest_cid"`
		UDSPath  string `json:"uds_path"`
	} `json:"vsock"`
}

func firecrackerConfiguration(record boxRecord, network boxNetwork,
	kernel, directory string) firecrackerDocument {
	var document firecrackerDocument
	document.BootSource.KernelImagePath = kernel
	document.BootSource.BootArguments = bootArguments(record, network)
	document.Drives = []firecrackerDrive{
		{"rootfs", filepath.Join(directory, boxDiskFile), true, false},
		{"config", filepath.Join(directory, configDiskFile), false, true},
	}
	document.MachineConfig.VCPUCount = record.CPUs
	document.MachineConfig.MemSizeMiB = record.MemoryMiB
	document.NetworkInterfaces = []firecrackerInterface{{
		"eth0", network.tapName(record.TapIndex),
		guestMAC(network, record.TapIndex),
	}}
	document.Vsock.GuestCID = guestCID
	document.Vsock.UDSPath = filepath.Join(directory, vsockSocketFile)
	return document
}

// bootArguments configures `eth0` in the kernel and starts `prison-guest`
// as PID 1. The kernel passes the words after `--` to init.
func bootArguments(record boxRecord, network boxNetwork) string {
	address := network.boxAddress(record.TapIndex)
	mask := net.IP(net.CIDRMask(network.subnet.Bits(), 32)).String()
	arguments := []string{"console=ttyS0", "reboot=k", "panic=1", "rw",
		fmt.Sprintf("ip=%s::%s:%s:%s:eth0:off", address, network.gateway,
			mask, record.Name),
		"init=" + guestInitPath}
	if runtime.GOARCH == "amd64" {
		arguments = append(arguments, "pci=off")
	}
	return strings.Join(append(arguments, "--", "init", "--machine"), " ")
}

func guestMAC(network boxNetwork, index int) string {
	octets := network.gateway.As4()
	return fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", octets[0], octets[1],
		octets[2], firstBoxHostNumber+index)
}

func memoryInMiB(size string) (int, error) {
	if size == "" {
		return defaultMemoryMiB, nil
	}
	units := map[byte]int{'M': 1, 'G': 1024, 'T': 1024 * 1024}
	trimmed := strings.TrimSuffix(strings.ToUpper(size), "B")
	if trimmed == "" {
		return 0, fmt.Errorf("memory size %q is not valid", size)
	}
	multiplier, found := units[trimmed[len(trimmed)-1]]
	number, err := strconv.Atoi(strings.TrimRight(trimmed, "MGT"))
	if !found || err != nil || number <= 0 {
		return 0, fmt.Errorf("memory size %q is not valid", size)
	}
	return number * multiplier, nil
}

// processOf returns the Firecracker PID of a box, or 0. The command line
// must name the API socket of the box, so a reused PID does not count.
func (driver *Driver) processOf(name string) int {
	directory := driver.boxDirectory(name)
	content, err := os.ReadFile(filepath.Join(directory, processFile))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		return 0
	}
	commandLine, err := os.ReadFile(
		filepath.Join(driver.procfs, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return 0
	}
	socket := filepath.Join(directory, apiSocketFile)
	if !bytes.Contains(commandLine, []byte(socket)) {
		return 0
	}
	return pid
}

func (driver *Driver) Stop(ctx context.Context, name string) error {
	if err := driver.stopProcess(ctx, name); err != nil {
		return err
	}
	record, found, err := driver.readBoxRecord(name)
	if err == nil && found {
		driver.unexportMounts(ctx, record)
	}
	return nil
}

func (driver *Driver) stopProcess(ctx context.Context, name string) error {
	pid := driver.processOf(name)
	if pid == 0 {
		return nil
	}
	socket := filepath.Join(driver.boxDirectory(name), vsockSocketFile)
	request, cancel := context.WithTimeout(ctx, 5*time.Second)
	askError := askAgent(request, socket, machine.OperationShutdown)
	cancel()
	if askError == nil && driver.waitForExit(ctx, name, driver.stopTimeout) {
		return nil
	}
	if err := unix.Kill(pid, unix.SIGKILL); err != nil &&
		!errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("cannot stop box %s: %w", name, err)
	}
	if !driver.waitForExit(ctx, name, 5*time.Second) {
		return fmt.Errorf("box %s did not stop", name)
	}
	return nil
}

func (driver *Driver) waitForExit(ctx context.Context, name string,
	timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for driver.processOf(name) != 0 {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true
}

func (driver *Driver) Delete(ctx context.Context, name string) error {
	if err := driver.requireStateDirectory(); err != nil {
		return err
	}
	if err := driver.Stop(ctx, name); err != nil {
		return err
	}
	unlock, err := driver.lockBoxes()
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.RemoveAll(driver.boxDirectory(name)); err != nil {
		return fmt.Errorf("cannot delete box %s: %w", name, err)
	}
	return nil
}

func (driver *Driver) Logs(
	ctx context.Context, name string, w io.Writer,
) error {
	file, err := os.Open(filepath.Join(driver.boxDirectory(name),
		consoleLogFile))
	if err != nil {
		return fmt.Errorf("cannot read the logs of box %s: %w", name, err)
	}
	defer file.Close()
	_, err = io.Copy(w, file)
	return err
}

func (driver *Driver) logTail(name string) string {
	var collected bytes.Buffer
	if err := driver.Logs(context.Background(), name, &collected); err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(collected.String(), "\n"), "\n")
	if len(lines) > logTailLines {
		lines = lines[len(lines)-logTailLines:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	return ":\n  " + strings.Join(lines, "\n  ")
}

func (driver *Driver) exportMounts(ctx context.Context, record boxRecord,
	network boxNetwork) error {
	client := network.boxAddress(record.TapIndex).String()
	for _, mount := range record.Mounts {
		err := nfsexport.Ask(ctx, nfsexport.SocketPath(driver.uid),
			nfsexport.Request{
				Operation: nfsexport.OperationExport,
				Path:      mount.Source,
				Client:    client,
				ReadOnly:  mount.ReadOnly,
			})
		if err != nil {
			return fmt.Errorf("cannot share %s with box %s: %w",
				mount.Source, record.Name, err)
		}
	}
	return nil
}

// unexportMounts ignores errors because the export can be missing.
func (driver *Driver) unexportMounts(ctx context.Context, record boxRecord) {
	network, err := networkFor(record.Network)
	if err != nil {
		return
	}
	client := network.boxAddress(record.TapIndex).String()
	for _, mount := range record.Mounts {
		nfsexport.Ask(ctx, nfsexport.SocketPath(driver.uid),
			nfsexport.Request{
				Operation: nfsexport.OperationUnexport,
				Path:      mount.Source,
				Client:    client,
			})
	}
}
