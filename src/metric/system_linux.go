//go:build linux

package metric

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// systemStats holds a single sample of system resource usage.
type systemStats struct {
	memUsed   uint64
	memTotal  uint64
	cpuIdle   uint64
	cpuTotal  uint64
	diskUsed  uint64
	diskTotal uint64
}

// systemStatsSupported reports whether system metrics can be collected.
func systemStatsSupported() bool { return true }

// readSystemStats samples memory (/proc/meminfo), CPU (/proc/stat), and disk
// (statfs on dataDir). It returns ok=false only when neither memory nor CPU
// could be read; disk is best-effort.
func readSystemStats(dataDir string) (systemStats, bool) {
	var st systemStats
	memOK := readMemInfo(&st)
	cpuOK := readCPUStat(&st)
	if dataDir != "" {
		readDisk(dataDir, &st)
	}
	return st, memOK || cpuOK
}

func readMemInfo(st *systemStats) bool {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	var total, avail uint64
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		// Values are in kB.
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = v * 1024
			haveTotal = true
		case "MemAvailable:":
			avail = v * 1024
			haveAvail = true
		}
		if haveTotal && haveAvail {
			break
		}
	}
	if !haveTotal {
		return false
	}
	st.memTotal = total
	if haveAvail && avail <= total {
		st.memUsed = total - avail
	}
	return true
}

func readCPUStat(st *systemStats) bool {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		var total uint64
		for i, fv := range fields {
			v, err := strconv.ParseUint(fv, 10, 64)
			if err != nil {
				continue
			}
			total += v
			// Field index 3 is idle, index 4 is iowait.
			if i == 3 || i == 4 {
				st.cpuIdle += v
			}
		}
		st.cpuTotal = total
		return true
	}
	return false
}

func readDisk(dataDir string, st *systemStats) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dataDir, &fs); err != nil {
		return
	}
	bsize := uint64(fs.Bsize)
	total := fs.Blocks * bsize
	free := fs.Bfree * bsize
	if total == 0 || free > total {
		return
	}
	st.diskTotal = total
	st.diskUsed = total - free
}
