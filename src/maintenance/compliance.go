// compliance.go implements the --maintenance compliance report subcommand
// (AI.md Compliance: "Operators run {project_name} --maintenance compliance
// report for a compliance summary"). Compliance is configured entirely in
// server.yml — this reads server.compliance.* and, for the standards with
// quantified values in AI.md's Requirements Matrix, resolves overlaps using
// the documented "strictest requirement wins" rule.
package maintenance

import (
	"fmt"
	"path/filepath"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/config"
)

// ComplianceOptions carries the resolved paths RunCompliance needs.
type ComplianceOptions struct {
	ConfigDir string
	LogDir    string
}

// standardRequirement is one row of AI.md's Compliance Requirements Matrix,
// covering only the standards for which the matrix gives quantified values.
type standardRequirement struct {
	name           string
	retentionYears int
	// breachNotifyHours is 0 when this standard does not specify a value.
	breachNotifyHours int
	// sessionTimeoutMins is 0 when this standard does not specify a value.
	sessionTimeoutMins int
	rightToErasure     bool
	dataPortability    bool
}

// RunCompliance executes a `--maintenance compliance <action>` subcommand.
// action is "report" — the only compliance action AI.md defines.
func RunCompliance(action string, opts ComplianceOptions) error {
	if action != "report" {
		return fmt.Errorf("%s", maintenanceText("compliance_unknown_action", "action", action))
	}

	cfgPath := filepath.Join(opts.ConfigDir, "server.yml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("compliance: load config: %w", err)
	}
	c := cfg.Server.Compliance

	enabled := map[string]bool{
		"GDPR":     c.GDPR,
		"CCPA":     c.CCPA,
		"HIPAA":    c.HIPAA,
		"SOC2":     c.SOC2,
		"PCI-DSS":  c.PCIDSS,
		"ISO27001": c.ISO27001,
		"FedRAMP":  c.FedRAMP,
		"LGPD":     c.LGPD,
		"PIPEDA":   c.PIPEDA,
		"APPI":     c.APPI,
		"PDPA":     c.PDPA,
	}
	order := []string{"GDPR", "CCPA", "HIPAA", "SOC2", "PCI-DSS", "ISO27001", "FedRAMP", "LGPD", "PIPEDA", "APPI", "PDPA"}

	fmt.Println(maintenanceText("compliance_heading"))
	var active []standardRequirement
	for _, name := range order {
		state := maintenanceText("compliance_disabled")
		if enabled[name] {
			state = maintenanceText("compliance_enabled")
		}
		fmt.Printf("  %-9s %s\n", name, state)
		if req, ok := quantifiedRequirements[name]; ok && enabled[name] {
			active = append(active, req)
		}
	}

	if len(active) > 0 {
		fmt.Println()
		fmt.Println(maintenanceText("compliance_resolved"))
		fmt.Printf("  %s\n", maintenanceText("compliance_retention", "value", strictestRetention(active)))
		if hrs := strictestBreachNotification(active); hrs > 0 {
			fmt.Printf("  %s\n", maintenanceText("compliance_breach", "value", hrs))
		}
		if mins := strictestSessionTimeout(active); mins > 0 {
			fmt.Printf("  %s\n", maintenanceText("compliance_session", "value", mins))
		}
		fmt.Printf("  %s\n", maintenanceText("compliance_erasure", "value", anyRightToErasure(active)))
		fmt.Printf("  %s\n", maintenanceText("compliance_portability", "value", anyDataPortability(active)))
	}

	ac := cfg.Server.Logging.Audit
	auditW := audit.New(audit.Config{
		Enabled:          ac.Enabled,
		Dir:              opts.LogDir,
		Filename:         ac.Filename,
		IncludeUserAgent: ac.IncludeUserAgent,
		MaskEmails:       ac.MaskEmails,
		Events: audit.EventCategories{
			Configuration: ac.Events.Configuration,
			Security:      ac.Events.Security,
			Backup:        ac.Events.Backup,
			Server:        ac.Events.Server,
		},
	})
	auditW.Log(audit.Entry{
		Event:    "compliance.report_generated",
		Severity: audit.SeverityInfo,
		Result:   audit.ResultSuccess,
		Details:  map[string]any{"report_type": "cli_summary"},
	})
	return nil
}

// quantifiedRequirements holds the standards for which AI.md's Requirements
// Matrix gives numeric values (GDPR, CCPA, HIPAA, SOC2, PCI-DSS, ISO27001).
// Standards outside this set (FedRAMP, LGPD, PIPEDA, APPI, PDPA) are
// descriptive-only in AI.md and are reported as enabled/disabled only.
var quantifiedRequirements = map[string]standardRequirement{
	"GDPR": {
		name: "GDPR", retentionYears: 1, breachNotifyHours: 72,
		rightToErasure: true, dataPortability: true,
	},
	"CCPA": {
		name: "CCPA", retentionYears: 1,
		rightToErasure: true, dataPortability: true,
	},
	"HIPAA": {
		name: "HIPAA", retentionYears: 6, breachNotifyHours: 60 * 24,
		sessionTimeoutMins: 15,
	},
	"SOC2": {
		name: "SOC2", retentionYears: 1, breachNotifyHours: 0,
	},
	"PCI-DSS": {
		name: "PCI-DSS", retentionYears: 1, sessionTimeoutMins: 15,
	},
	"ISO27001": {
		name: "ISO27001", retentionYears: 3,
	},
}

func strictestRetention(reqs []standardRequirement) int {
	max := 0
	for _, r := range reqs {
		if r.retentionYears > max {
			max = r.retentionYears
		}
	}
	return max
}

func strictestBreachNotification(reqs []standardRequirement) int {
	min := 0
	for _, r := range reqs {
		if r.breachNotifyHours <= 0 {
			continue
		}
		if min == 0 || r.breachNotifyHours < min {
			min = r.breachNotifyHours
		}
	}
	return min
}

func strictestSessionTimeout(reqs []standardRequirement) int {
	min := 0
	for _, r := range reqs {
		if r.sessionTimeoutMins <= 0 {
			continue
		}
		if min == 0 || r.sessionTimeoutMins < min {
			min = r.sessionTimeoutMins
		}
	}
	return min
}

func anyRightToErasure(reqs []standardRequirement) bool {
	for _, r := range reqs {
		if r.rightToErasure {
			return true
		}
	}
	return false
}

func anyDataPortability(reqs []standardRequirement) bool {
	for _, r := range reqs {
		if r.dataPortability {
			return true
		}
	}
	return false
}
