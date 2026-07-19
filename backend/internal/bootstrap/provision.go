package bootstrap

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
)

//go:embed provision.sh
var provisionScript string

// fcUser is the dedicated non-root user Firecracker VMMs drop to.
const fcUser = "fcvm"

// ProvisionParams are the host-specific paths the embedded provision.sh needs.
// They come straight from the agent's resolved config so the script and the Go
// runtime agree on exactly which dirs to own.
type ProvisionParams struct {
	Root        string
	SlotCount   int
	SocketDir   string
	SnapshotDir string
	AssetsDir   string
}

// Provision makes the host able to run microVMs with no external setup: the
// fcvm user, socket/snapshot dirs, asset ACLs, and SlotCount network slots
// (netns + TAP + veth + nftables). It runs the embedded provision.sh as root.
// Idempotent, with a verify fast-path so an agent restart does not tear down
// slots that are already in place. Returns the fcvm uid/gid the VMMs drop to.
func Provision(p ProvisionParams) (uid, gid int, err error) {
	if os.Geteuid() != 0 {
		return 0, 0, fmt.Errorf("bootstrap: provisioning requires root (start the agent via sudo)")
	}
	if u, g, ok := alreadyProvisioned(p.SlotCount); ok {
		return u, g, nil
	}

	cmd := exec.Command("bash", "-s")
	cmd.Stdin = strings.NewReader(provisionScript)
	cmd.Env = append(os.Environ(),
		"ROOT_DIRECTORY="+p.Root,
		"SLOT_COUNT="+strconv.Itoa(p.SlotCount),
		"FC_USER="+fcUser,
		"ASSETS_DIR="+p.AssetsDir,
		"SOCKET_DIR="+p.SocketDir,
		"SNAPSHOT_DIR="+p.SnapshotDir,
	)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return 0, 0, fmt.Errorf("bootstrap: provision.sh failed: %w\n%s", runErr, out)
	}
	return fcvmIDs()
}

// alreadyProvisioned reports whether the fcvm user, the nftables `inet fc`
// table, and exactly slotCount fc-ns-* namespaces already exist — the signal
// that a prior run provisioned this host, so we skip the destructive re-run.
func alreadyProvisioned(slotCount int) (uid, gid int, ok bool) {
	u, g, err := fcvmIDs()
	if err != nil {
		return 0, 0, false
	}
	if exec.Command("nft", "list", "table", "inet", "fc").Run() != nil {
		return 0, 0, false
	}
	out, err := exec.Command("ip", "netns", "list").Output()
	if err != nil {
		return 0, 0, false
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "fc-ns-") {
			n++
		}
	}
	if n != slotCount {
		return 0, 0, false
	}
	return u, g, true
}

func fcvmIDs() (uid, gid int, err error) {
	u, err := user.Lookup(fcUser)
	if err != nil {
		return 0, 0, err
	}
	if uid, err = strconv.Atoi(u.Uid); err != nil {
		return 0, 0, err
	}
	if gid, err = strconv.Atoi(u.Gid); err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}
