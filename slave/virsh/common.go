package virsh

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	grpcVirsh "github.com/Maruqes/512SvMan/api/proto/virsh"
	libvirt "libvirt.org/go/libvirt"
)

const ROOTFOLDER = "/var/512SvMan"

func dirQCOW2() string { return filepath.Join(ROOTFOLDER, "qcow2") }
func dirISO() string   { return filepath.Join(ROOTFOLDER, "iso") }
func dirXML() string   { return filepath.Join(ROOTFOLDER, "xml") }

func EnsureDirs() error {
	for _, d := range []string{ROOTFOLDER, dirQCOW2(), dirISO(), dirXML()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}

// toAbsUnderRoot: keep absolute as-is; relative -> join to defaultDir.
func toAbsUnderRoot(defaultDir, nameOrPath string) string {
	if nameOrPath == "" {
		return defaultDir
	}
	if filepath.IsAbs(nameOrPath) {
		return nameOrPath
	}
	return filepath.Join(defaultDir, nameOrPath)
}

// Resolve disk & ISO paths under ROOTFOLDER if relative
func ResolveDiskPath(p string) string { return toAbsUnderRoot(dirQCOW2(), p) }
func ResolveISOPath(p string) string  { return toAbsUnderRoot(dirISO(), p) }

// qemu-img --output=json minimal struct
type qiInfo struct {
	Format string `json:"format"`
}

// DetectDiskFormat returns "qcow2" or "raw" (or other qemu formats if present).
// If the file doesn't exist, it infers from the extension.
func DetectDiskFormat(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		cmd := exec.Command("qemu-img", "info", "--output=json", path)
		out, err := cmd.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return "", fmt.Errorf("qemu-img info %s: %s", path, strings.TrimSpace(string(exitErr.Stderr)))
			}
			return "", fmt.Errorf("qemu-img info %s: %w", path, err)
		}
		var info qiInfo
		if err := json.Unmarshal(out, &info); err != nil {
			return "", fmt.Errorf("parse qemu-img info json: %w", err)
		}
		if info.Format == "" {
			return "", fmt.Errorf("could not detect disk format for %s", path)
		}
		return strings.ToLower(info.Format), nil
	}

	// Not exists: infer by extension
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".qcow2":
		return "qcow2", nil
	case ".img", ".raw":
		return "raw", nil
	default:
		// default to qcow2 if unknown
		return "qcow2", nil
	}
}

// EnsureDiskAndDetectFormat creates the disk if missing (using detected format)
// and returns the resulting format (e.g. "qcow2" or "raw").
func EnsureDiskAndDetectFormat(path string, sizeGB int) (string, error) {
	fmtStr, err := DetectDiskFormat(path)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(path); err == nil {
		return fmtStr, nil
	}

	if sizeGB <= 0 {
		return "", fmt.Errorf("disk %s does not exist and sizeGB <= 0", path)
	}

	// Create with chosen format
	args := []string{"create", "-f", fmtStr, path, fmt.Sprintf("%dG", sizeGB)}
	cmd := exec.Command("qemu-img", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return "", fmt.Errorf("qemu-img create %s: %s", path, msg)
		}
		return "", fmt.Errorf("qemu-img create %s: %w", path, err)
	}
	return fmtStr, nil
}

// WriteDomainXMLToDisk: save vm XML under xml/<vm>.xml
func WriteDomainXMLToDisk(vmName, xml string) (string, error) {
	if err := EnsureDirs(); err != nil {
		return "", err
	}
	out := filepath.Join(dirXML(), fmt.Sprintf("%s.xml", vmName))
	if err := os.WriteFile(out, []byte(strings.TrimSpace(xml)+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("write xml %s: %w", out, err)
	}
	return out, nil
}

func RestartLibvirt() error {
	cmd := exec.Command("systemctl", "restart", "libvirtd")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("restart libvirtd: %s", msg)
		}
		return fmt.Errorf("restart libvirtd: %w", err)
	}
	return nil
}

// # qemu:///system  => system libvirtd (/etc/libvirt/qemu.conf)
// set-> remote_display_port_min = 12000
//
//	remote_display_port_max = 12999
func SetVNCPorts(minPort, maxPort int) error {
	if minPort < 5900 || maxPort > 65535 || minPort > maxPort {
		return fmt.Errorf("invalid remote display port range %d-%d", minPort, maxPort)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("SetVNCPorts requires root privileges")
	}

	const configPath = "/etc/libvirt/qemu.conf"

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}

	content := string(data)
	minLine := fmt.Sprintf("remote_display_port_min = %d", minPort)
	maxLine := fmt.Sprintf("remote_display_port_max = %d", maxPort)

	minPattern := regexp.MustCompile(`(?i)^\s*#?\s*remote_display_port_min\s*=`)
	maxPattern := regexp.MustCompile(`(?i)^\s*#?\s*remote_display_port_max\s*=`)

	trimmed := strings.TrimRight(content, "\n")
	var lines []string
	if trimmed != "" {
		lines = strings.Split(trimmed, "\n")
	}

	var out []string
	minApplied := false
	maxApplied := false
	for _, line := range lines {
		switch {
		case minPattern.MatchString(line):
			if !minApplied {
				out = append(out, minLine)
				minApplied = true
			}
		case maxPattern.MatchString(line):
			if !maxApplied {
				out = append(out, maxLine)
				maxApplied = true
			}
		default:
			out = append(out, line)
		}
	}

	if !minApplied {
		out = append(out, minLine)
		minApplied = true
	}
	if !maxApplied {
		out = append(out, maxLine)
		maxApplied = true
	}

	newContent := strings.Join(out, "\n")
	if newContent != "" {
		newContent += "\n"
	}

	if newContent == content {
		return nil
	}

	info, err := os.Stat(configPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", configPath, err)
	}
	if err := os.WriteFile(configPath, []byte(newContent), info.Mode().Perm()); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}

	if err := RestartLibvirt(); err != nil {
		return fmt.Errorf("restart libvirt after updating %s: %w", configPath, err)
	}

	return nil
}
func domainStateToString(state libvirt.DomainState) string {
	switch state {
	case libvirt.DOMAIN_NOSTATE:
		return "no state"
	case libvirt.DOMAIN_RUNNING:
		return "running"
	case libvirt.DOMAIN_BLOCKED:
		return "blocked"
	case libvirt.DOMAIN_PAUSED:
		return "paused"
	case libvirt.DOMAIN_SHUTDOWN:
		return "shutdown"
	case libvirt.DOMAIN_SHUTOFF:
		return "shut off"
	case libvirt.DOMAIN_CRASHED:
		return "crashed"
	case libvirt.DOMAIN_PMSUSPENDED:
		return "suspended"
	default:
		return "unknown"
	}
}

func GetVMByName(name string) (*grpcVirsh.Vm, error) {
	connURI := "qemu:///system"
	conn, err := libvirt.NewConnect(connURI)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	dom, err := conn.LookupDomainByName(name)
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	defer dom.Free()

	// Get state
	state, _, err := dom.GetState()
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}

	// Parse XML for VNC port
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return nil, fmt.Errorf("xml: %w", err)
	}

	port := 0
	if strings.Contains(xmlDesc, "graphics type='vnc'") {
		start := strings.Index(xmlDesc, "port='")
		if start != -1 {
			fmt.Sscanf(xmlDesc[start:], "port='%d'", &port)
		}
	}

	info := &grpcVirsh.Vm{
		Name:      name,
		State:     domainStateToString(state),
		NovncPort: strconv.Itoa(port),
	}
	return info, nil
}

func GetAllVMs() ([]*grpcVirsh.Vm, error) {
	connURI := "qemu:///system"
	conn, err := libvirt.NewConnect(connURI)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	doms, err := conn.ListAllDomains(0)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}

	var vms []*grpcVirsh.Vm
	for _, dom := range doms {
		name, err := dom.GetName()
		if err != nil {
			dom.Free()
			return nil, fmt.Errorf("get name: %w", err)
		}

		state, _, err := dom.GetState()
		if err != nil {
			dom.Free()
			return nil, fmt.Errorf("state: %w", err)
		}

		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			dom.Free()
			return nil, fmt.Errorf("xml: %w", err)
		}

		port := 0
		if strings.Contains(xmlDesc, "graphics type='vnc'") {
			start := strings.Index(xmlDesc, "port='")
			if start != -1 {
				fmt.Sscanf(xmlDesc[start:], "port='%d'", &port)
			}
		}

		info := &grpcVirsh.Vm{
			Name:      name,
			State:     domainStateToString(state),
			NovncPort: strconv.Itoa(port),
		}
		vms = append(vms, info)
		dom.Free()
	}
	return vms, nil
}
