package metric

import (
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

// systemStats holds one portable sample of system resource usage.
type systemStats struct {
	memUsed   uint64
	memTotal  uint64
	cpuUsage  float64
	cpuValid  bool
	diskUsed  uint64
	diskTotal uint64
}

// systemStatsSupported reports whether this build can collect system metrics.
// gopsutil supports the project's Linux, macOS, Windows, and BSD targets.
func systemStatsSupported() bool { return true }

func readSystemStats(dataDir string) (systemStats, bool) {
	var st systemStats
	if values, err := cpu.Percent(0, false); err == nil && len(values) > 0 {
		st.cpuUsage = values[0]
		st.cpuValid = true
	}
	if values, err := mem.VirtualMemory(); err == nil {
		st.memUsed = values.Used
		st.memTotal = values.Total
	}
	if dataDir != "" {
		if usage, err := disk.Usage(dataDir); err == nil {
			st.diskUsed = usage.Used
			st.diskTotal = usage.Total
		}
	}
	return st, st.cpuValid || st.memTotal > 0
}
