package nfs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Maruqes/512SvMan/logger"
)

type FolderMount struct {
	FolderPath string // shared folder, folder in host that will be shared via nfs
	Source     string // nfs source (ip:/path)
	Target     string // local mount point
}

const (
	monitorInterval         = 5 * time.Second
	monitorFailureThreshold = 3
	exportsDir              = "/etc/exports.d"
	exportsFile             = "/etc/exports.d/512svman.exports"
)

var CurrentMounts = []FolderMount{}
var CurrentMountsLock = &sync.RWMutex{}

func listFilesInDir(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	files, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	return files, nil
}

func isMounted(target string) bool {
	_, err := listFilesInDir(target)
	if err != nil {
		// If we can't read the directory, assume it's not mounted
		logger.Error("cannot read mount target:", target, err)
		return false
	}
	// Use the mountpoint command to check if the target is a mount point
	out, err := exec.Command("mountpoint", "-q", target).CombinedOutput()
	if err != nil {
		logger.Error("mountpoint check failed:", err, string(out))
		return false
	}
	return true
}

func MonitorMounts() {
	for {
		CurrentMountsLock.RLock()
		for _, mount := range CurrentMounts {
			//check if is mounted
			if !isMounted(mount.Target) {
				//if not try to mount 3 times with 5 seconds interval
				logger.Warn("NFS mount lost, attempting to remount:", mount.Target)
				success := false
				for i := 0; i < monitorFailureThreshold; i++ {
					err := MountSharedFolder(mount)
					if err == nil {
						success = true
						break
					}
					time.Sleep(monitorInterval)
				}
				if !success {
					logger.Error("Failed to remount NFS share after multiple attempts:", mount.Target)
					err := UnmountSharedFolder(mount)
					if err != nil {
						logger.Error("Failed to unmount NFS share:", mount.Target, err)
					}
					logger.Error("NFS share unmounted to prevent further issues:", mount.Target)
				} else {
					logger.Info("Successfully remounted NFS share:", mount.Target)
				}
			}
		}
		CurrentMountsLock.RUnlock()
		time.Sleep(monitorInterval)
	}
}

func InstallNFS() error {
	if err := runCommand("install nfs-utils", "sudo", "dnf", "-y", "install", "nfs-utils"); err != nil {
		return err
	}
	// Ensure the NFS server services are available when acting as a host
	if err := runCommand("enable nfs-server", "sudo", "systemctl", "enable", "--now", "nfs-server"); err != nil {
		return err
	}
	logger.Info("NFS installed and nfs-server enabled")
	go MonitorMounts()
	return nil
}

func exportsEntry(path string) string {
	return fmt.Sprintf("%s *(rw,sync,no_subtree_check,no_root_squash)", path)
}

func allowSELinuxForNFS(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("selinux path is required")
	}

	if !commandExists("getenforce") {
		return nil
	}

	modeOut, err := exec.Command("getenforce").Output()
	if err != nil {
		logger.Warn("SELinux detection failed, skipping adjustments:", err)
		return nil
	}
	mode := strings.TrimSpace(string(modeOut))
	if strings.EqualFold(mode, "disabled") {
		return nil
	}

	if err := ensureNFSSELinuxBoolean(); err != nil {
		return err
	}

	if err := labelNFSMountSource(path); err != nil {
		return err
	}

	if err := ensureNFSGeneratorPolicy(); err != nil {
		return err
	}

	return nil
}

// CreateSharedFolder creates a directory and ensures it is exported via exportsFile.
func CreateSharedFolder(folder FolderMount) error {
	if !filepath.IsAbs(folder.FolderPath) {
		return fmt.Errorf("folder path must be a full path and exist")
	}

	path := strings.TrimSpace(folder.FolderPath)
	if path == "" {
		return fmt.Errorf("folder path is required")
	}

	if err := runCommand("create share directory", "sudo", "mkdir", "-p", path); err != nil {
		return err
	}
	
	if err := allowSELinuxForNFS(path); err != nil {
		return err
	}

	entry := exportsEntry(path)
	cmdStr := fmt.Sprintf("mkdir -p %s && touch %s && (grep -Fxq %q %s || echo %q >> %s)", exportsDir, exportsFile, entry, exportsFile, entry, exportsFile)
	if err := runCommand("update NFS exports", "sudo", "bash", "-lc", cmdStr); err != nil {
		return err
	}

	if err := runCommand("refresh nfs exports", "sudo", "exportfs", "-ra"); err != nil {
		return err
	}
	logger.Info("NFS share created: " + path)
	return nil
}

func RemoveSharedFolder(folder FolderMount) error {
	path := strings.TrimSpace(folder.FolderPath)
	if path == "" {
		return fmt.Errorf("folder path is required")
	}

	// 1) Remove any export line whose first field equals the path
	//    - Keeps comments/blank lines intact
	//    - Robust to different export options/spacing on the line
	filterCmd := fmt.Sprintf(`
set -euo pipefail
file='%s'
if [ -f "$file" ]; then
  tmp=$(mktemp)
  awk -v p='%s' 'BEGIN{OFS=FS=" "}{ if ($0 ~ /^[[:space:]]*#/ || NF==0) { print; next } if ($1!=p) { print } }' "$file" > "$tmp"
  install -m 0644 "$tmp" "$file"
  rm -f "$tmp"
fi
`, escapeForSingleQuotes(exportsFile), escapeForSingleQuotes(path))
	if err := runCommand("filter NFS exports", "sudo", "bash", "-lc", filterCmd); err != nil {
		return err
	}

	// 2) Unexport this path if it’s currently exported (ignores error if not exported)
	_ = runCommand("unexport path", "sudo", "exportfs", "-u", path)

	// 3) Re-apply exports
	if err := runCommand("refresh nfs exports", "sudo", "exportfs", "-ra"); err != nil {
		return err
	}

	logger.Info("NFS share removed:", path)
	return nil
}

// Escapes a string for safe inclusion inside single quotes in a POSIX shell.
// Example: abc'def -> 'abc'"'"'def'
func escapeForSingleQuotes(s string) string {
	return strings.ReplaceAll(s, `'`, `'"'"'`)
}

// MountSharedFolder mounts an exported folder from the network.
func MountSharedFolder(folder FolderMount) error {
	source := strings.TrimSpace(folder.Source)
	target := strings.TrimSpace(folder.Target)
	if source == "" {
		return fmt.Errorf("source is required")
	}
	if target == "" {
		return fmt.Errorf("target is required")
	}

	if err := runCommand("ensure mount directory", "sudo", "mkdir", "-p", target); err != nil {
		return err
	}

	mountOptions := []string{"_netdev", "soft", "timeo=10", "retrans=2", "nofail", "vers=4"}
	// Use aggressive timeouts so that we notice server failures quickly.
	if err := runCommand("mount nfs share", "sudo", "mount", "-t", "nfs", "-o", strings.Join(mountOptions, ","), source, target); err != nil {
		return err
	}

	logger.Info("NFS share mounted: " + source + " -> " + target)
	CurrentMountsLock.Lock()
	CurrentMounts = append(CurrentMounts, folder)
	CurrentMountsLock.Unlock()
	return nil
}

func UnmountSharedFolder(folder FolderMount) error {
	target := strings.TrimSpace(folder.Target)
	if target == "" {
		return fmt.Errorf("target is required")
	}

	if err := runCommand("unmount nfs share", "sudo", "umount", target); err != nil {
		return err
	}
	logger.Info("NFS share unmounted: " + target)
	CurrentMountsLock.Lock()
	defer CurrentMountsLock.Unlock()
	for i, m := range CurrentMounts {
		if m.Target == target {
			CurrentMounts = append(CurrentMounts[:i], CurrentMounts[i+1:]...)
			break
		}
	}
	return nil
}

func runCommand(desc string, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s: no command provided", desc)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		logger.Error(desc + " failed: " + err.Error())
		return fmt.Errorf("%s: %w", desc, err)
	}
	logger.Info(desc + " succeeded")
	return nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func ensureNFSSELinuxBoolean() error {
	if !commandExists("setsebool") {
		logger.Warn("setsebool binary not available, skipping SELinux boolean for NFS exports")
		return nil
	}
	if err := runCommand("enable nfs_export_all_rw", "sudo", "setsebool", "-P", "nfs_export_all_rw", "on"); err != nil {
		return err
	}
	return nil
}

func labelNFSMountSource(path string) error {
	if !commandExists("semanage") || !commandExists("restorecon") {
		logger.Warn("semanage or restorecon missing, skipping SELinux labeling for", path)
		return nil
	}
	pattern := fmt.Sprintf("%s(/.*)?", strings.TrimRight(path, "/"))
	escapedPattern := escapeForSingleQuotes(pattern)
	cmd := fmt.Sprintf("semanage fcontext -a -t public_content_rw_t '%s' || semanage fcontext -m -t public_content_rw_t '%s'", escapedPattern, escapedPattern)
	if err := runCommand("label selinux context for share", "sudo", "bash", "-lc", cmd); err != nil {
		return err
	}
	if err := runCommand("restore selinux context", "sudo", "restorecon", "-Rv", path); err != nil {
		return err
	}
	return nil
}

func ensureNFSGeneratorPolicy() error {
	// Module names must begin with a letter per SELinux policy syntax rules.
	const moduleName = "svman_nfs_generator"
	if !commandExists("semodule") || !commandExists("checkmodule") || !commandExists("semodule_package") {
		logger.Warn("SELinux policy tools missing, skipping custom module installation")
		return nil
	}

	listCmd := exec.Command("semodule", "-l")
	out, err := listCmd.Output()
	if err == nil && strings.Contains(string(out), moduleName) {
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "selinux-module-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	tePath := filepath.Join(tmpDir, moduleName+".te")
	teContent := fmt.Sprintf(`module %s 1.0;

require {
    type systemd_nfs_generator_t;
    class capability dac_read_search;
}

allow systemd_nfs_generator_t systemd_nfs_generator_t:capability dac_read_search;
`, moduleName)
	if err := os.WriteFile(tePath, []byte(teContent), 0644); err != nil {
		return err
	}

	modPath := filepath.Join(tmpDir, moduleName+".mod")
	ppPath := filepath.Join(tmpDir, moduleName+".pp")

	if err := runCommand("compile selinux policy module", "sudo", "checkmodule", "-M", "-m", "-o", modPath, tePath); err != nil {
		return err
	}
	if err := runCommand("package selinux policy module", "sudo", "semodule_package", "-o", ppPath, "-m", modPath); err != nil {
		return err
	}
	if err := runCommand("install selinux policy module", "sudo", "semodule", "-X", "300", "-i", ppPath); err != nil {
		return err
	}
	return nil
}
