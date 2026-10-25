package library

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Include nested mounts: a disconnected submount must quarantine its parent
// snapshot too. Marker checks additionally detect an empty underlying directory.
func sourceRootIdentity(root string, info os.FileInfo) string {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "unverified:" + sourceFileIdentity(info)
	}
	return sourceMountIdentity(root, info, data)
}

func sourceMountIdentity(root string, info os.FileInfo, data []byte) string {
	return stableSourceMountIdentity(root, info, data, sourceFilesystemIdentity)
}
func stableSourceMountIdentity(root string, info os.FileInfo, data []byte, filesystem func(string) string) string {
	var relevant []string
	var covering string
	longest := -1
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		signature := strings.Join(fields[3:5], " ")
		for i, f := range fields {
			if f == "-" && i+2 < len(fields) {
				signature += " " + fields[i+1]
				break
			}
		}
		mount := fields[4]
		if decoded, e := strconv.Unquote(`"` + mount + `"`); e == nil {
			mount = decoded
		}
		if !sourceWithin(root, mount) && !sourceWithin(mount, root) {
			continue
		}
		fsid := filesystem(mount)
		// Failure to inspect an involved mount must change the identity, never
		// silently accept a weaker marker/inode-only identity.
		if fsid == "" {
			fsid = "unverified:" + fields[2]
		}
		signature += " " + fsid
		if mount != root && sourceWithin(mount, root) {
			relevant = append(relevant, signature)
		}
		if sourceWithin(root, mount) && len(mount) > longest {
			longest = len(mount)
			covering = signature
		}
	}
	sort.Strings(relevant)
	topology := covering + "\n" + strings.Join(relevant, "\n")
	sum := sha256.Sum256([]byte(topology))
	if covering == "" || strings.Contains(topology, "unverified:") {
		return fmt.Sprintf("unverified:%x", sum)
	}
	inode := sourceFileIdentity(info)
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		inode = fmt.Sprint(st.Ino)
	}
	return fmt.Sprintf("v3:%s:%x", inode, sum)
}
