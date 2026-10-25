package library

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// FS_IOC_GETFSUUID = _IOR(0x15, 0, struct fsuuid2), a 17-byte Linux UAPI
// structure. It works on a read-only directory fd without exposing block devices.
const sourceGetFSUUID = 0x80111500

func sourceFilesystemUUID(path string) []byte {
	f, e := os.Open(path)
	if e != nil {
		return nil
	}
	defer f.Close()
	var raw [17]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), uintptr(sourceGetFSUUID), uintptr(unsafe.Pointer(&raw[0])))
	runtime.KeepAlive(&raw)
	if errno != 0 || raw[0] == 0 || raw[0] > 16 {
		return nil
	}
	return append([]byte(nil), raw[1:1+int(raw[0])]...)
}
func sourceFilesystemIdentity(path string) string {
	var stat unix.Statfs_t
	if e := unix.Statfs(path, &stat); e != nil {
		return ""
	}
	info, e := os.Stat(path)
	if e != nil {
		return ""
	}
	device := uint64(0)
	if s, ok := info.Sys().(*syscall.Stat_t); ok {
		device = s.Dev
	}
	return stableFilesystemIdentity(uint64(stat.Type), stat.Fsid.Val, device, sourceFilesystemUUID(path))
}
func stableFilesystemIdentity(kind uint64, fsid [2]int32, device uint64, uuid []byte) string {
	kind &= 0xffffffff
	if len(uuid) > 0 && len(uuid) <= 16 {
		nonzero := false
		for _, b := range uuid {
			nonzero = nonzero || b != 0
		}
		if nonzero {
			return fmt.Sprintf("%x:uuid:%x", kind, uuid)
		}
	}
	id := uint64(uint32(fsid[0])) | uint64(uint32(fsid[1]))<<32
	// F2FS exports huge_encode_dev() as f_fsid, so using it merely hides the
	// same volatile device number inside a digest. Never trust that fallback.
	if kind == 0xf2f52010 || id == 0 || id == device {
		return ""
	}
	return fmt.Sprintf("%x:fsid:%x", kind, id)
}
