package maintenance

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/database"
	"gopkg.in/yaml.v3"
)

var schedulerTaskIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// SetSchedulerTaskEnabled stores a task's enabled state in the maintenance
// drop-in, the canonical configuration source for scheduler settings.
func SetSchedulerTaskEnabled(configDir, taskID string, enabled bool) error {
	if !schedulerTaskIDPattern.MatchString(taskID) {
		return fmt.Errorf("invalid scheduler task id %q", taskID)
	}
	dir := filepath.Join(configDir, "yaml.d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating maintenance config directory: %w", err)
	}
	path := filepath.Join(dir, "maintenance.yml")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading maintenance config: %w", err)
	}
	var doc yaml.Node
	if len(data) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	} else if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing maintenance config: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("maintenance config must contain a YAML mapping")
	}
	root := doc.Content[0]
	server, err := ensureYAMLMapping(root, "server")
	if err != nil {
		return err
	}
	scheduler, err := ensureYAMLMapping(server, "scheduler")
	if err != nil {
		return err
	}
	tasks, err := ensureYAMLMapping(scheduler, "tasks")
	if err != nil {
		return err
	}
	task, err := ensureYAMLMapping(tasks, taskID)
	if err != nil {
		return err
	}
	value := "false"
	if enabled {
		value = "true"
	}
	setYAMLMappingValue(task, "enabled", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: value})
	encoded, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("encoding maintenance config: %w", err)
	}
	if err := atomicWriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("writing maintenance config: %w", err)
	}
	return nil
}

func ensureYAMLMapping(parent *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			if parent.Content[i+1].Kind != yaml.MappingNode {
				return nil, fmt.Errorf("maintenance config key %q must contain a mapping", key)
			}
			return parent.Content[i+1], nil
		}
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setYAMLMappingValue(parent, key, child)
	return child, nil
}

func setYAMLMappingValue(parent *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1] = value
			return
		}
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// PrintSchedulerTasks writes the scheduler overview in stable task-id order.
func PrintSchedulerTasks(w io.Writer, tasks []database.TaskState) {
	tasks = append([]database.TaskState(nil), tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	fmt.Fprintf(w, "%-24s %-8s %-18s %-20s %-20s %-12s %8s %8s\n", "TASK", "ENABLED", "SCHEDULE", "LAST RUN", "NEXT RUN", "STATUS", "RUNS", "FAILED")
	fmt.Fprintf(w, "%-24s %-8s %-18s %-20s %-20s %-12s %8s %8s\n", strings.Repeat("-", 24), strings.Repeat("-", 8), strings.Repeat("-", 18), strings.Repeat("-", 20), strings.Repeat("-", 20), strings.Repeat("-", 12), strings.Repeat("-", 8), strings.Repeat("-", 8))
	for _, task := range tasks {
		enabled := "no"
		if task.Enabled {
			enabled = "yes"
		}
		fmt.Fprintf(w, "%-24s %-8s %-18s %-20s %-20s %-12s %8d %8d\n",
			task.TaskID, enabled, task.Schedule, schedulerTime(task.LastRun), schedulerTime(task.NextRun), task.LastStatus, task.RunCount, task.FailCount)
	}
}

// PrintSchedulerTask writes one task's status and latest persisted error.
func PrintSchedulerTask(w io.Writer, task database.TaskState) {
	fmt.Fprintf(w, "Task: %s\nEnabled: %t\nSchedule: %s\nStatus: %s\nLast run: %s\nNext run: %s\nRuns: %d\nFailed: %d\n",
		task.TaskID, task.Enabled, task.Schedule, task.LastStatus, schedulerTime(task.LastRun), schedulerTime(task.NextRun), task.RunCount, task.FailCount)
	if task.LastError != "" {
		fmt.Fprintf(w, "Last error: %s\n", task.LastError)
	}
}

// PrintSchedulerHistory writes task history newest first.
func PrintSchedulerHistory(w io.Writer, history []*database.TaskHistory) {
	fmt.Fprintf(w, "%-20s %-12s %-12s %s\n", "STARTED", "DURATION", "STATUS", "ERROR")
	for _, run := range history {
		fmt.Fprintf(w, "%-20s %-12s %-12s %s\n", schedulerTime(run.StartedAt), (time.Duration(run.DurationMS) * time.Millisecond).String(), run.Status, run.ErrorMsg)
	}
}

func schedulerTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format("2006-01-02 15:04:05 MST")
}
