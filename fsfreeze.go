package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// from linux/fs.h
const ioctlFIFREEZE = 0xC0045877
const ioctlFITHAW = 0xC0045878

// fsfreeze spec constants defined by the QEMU Guest Agent Protocol spec.
const guestFsfreezeStatusThawed = "thawed"
const guestFsfreezeStatusFrozen = "frozen"

// Describes the system freeze status.
var guestFsfreezeStatus string

// mEntry represents a mountpoint entry from /proc/self/mounts.
type mEntry struct {
	fsSpec string
	fsFile string
	fsType string
}

// fsfreezeFreeze freezes the freezable mounted filesystems.
// It returns an int which represents the number of of successfully frozen filesystems.
func fsfreezeFreeze() (int, error) {
	frozenFSCount := 0
	m, err := getMountPoints()
	if err != nil {
		return 0, fmt.Errorf("error while getting mountpoints to fsfreeze-freeze: %v", err)
	}

	for _, mp := range m {
		fs, err := os.Open(mp.fsFile)
		if err != nil {
			return 0, fmt.Errorf("error while opening mount path for fsfreeze-freeze: %v", err)
		}

		if err := ioctl(fs.Fd(), ioctlFIFREEZE); err != nil &&
			!errors.Is(err, syscall.EOPNOTSUPP) &&
			!errors.Is(err, syscall.EBUSY) {
			fs.Close()
			return 0, fmt.Errorf("error while executing fsfreeze-freeze on mount path %q: %v", mp.fsFile, err)
		}
		fs.Close()

		frozenFSCount++
	}

	return frozenFSCount, nil
}

// fsfreezeThaw unfreezes the frozen filesystems.
// It returns an int which represents the number of of successfully thawed filesystems.
func fsfreezeThaw() (int, error) {
	thawedFSCount := 0

	m, err := getMountPoints()
	if err != nil {
		return 0, fmt.Errorf("error while getting mountpoints to fsfreeze-thaw: %v", err)
	}

	for _, mp := range m {
		fs, err := os.Open(mp.fsFile)
		if err != nil {
			return 0, fmt.Errorf("error while opening mount path for fsfreeze-thaw: %v", err)
		}

		if err := ioctl(fs.Fd(), ioctlFITHAW); err != nil &&
			!errors.Is(err, syscall.EINVAL) {
			fs.Close()
			return 0, fmt.Errorf("error while executing fsfreeze-thaw on mount path %q: %v", mp.fsFile, err)
		}
		fs.Close()

		thawedFSCount++
	}

	return thawedFSCount, nil
}

// getMountPoints returns the freezable filesystems' mountpoints.
func getMountPoints() ([]mEntry, error) {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	m := make([]mEntry, 0, 10)

	isExist := func(fsspec string) bool {
		for _, v := range m {
			if v.fsSpec == fsspec {
				return true
			}
		}
		return false
	}

	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// Ignore non relevant filesystems.
		if len(fields) < 2 || isExist(fields[0]) || fields[0][0] != '/' ||
			fields[2] == "smbfs" || fields[2] == "cifs" {
			continue
		}

		// Ignore /dev/root device, in gokrazy it is a read-only filesystem, no point in freezing it.
		if strings.HasPrefix(fields[0], "/dev/root") {
			continue
		}
		// Ignore loop devices.
		if strings.HasPrefix(fields[0], "/dev/loop") {
			continue
		}
		// Ignore dm- devices.
		st, err := os.Lstat(fields[0])
		if err != nil {
			return nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			if s, err := os.Readlink(fields[0]); err != nil {
				return nil, err
			} else {
				fields[0] = filepath.Base(s)
			}
		}
		if strings.HasPrefix(fields[0], "dm-") {
			continue
		}

		m = append(m, mEntry{fields[0], fields[1], fields[2]})
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}

	return m, nil
}

func ioctl(fd uintptr, request uintptr) (err error) {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, 0)
	if errno != 0 {
		err = errno
	}

	return os.NewSyscallError("ioctl", err)
}
