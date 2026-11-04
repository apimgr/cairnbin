package email

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// maxSubjectLen is the RFC 5322 recommended line length; longer subjects only warn.
const maxSubjectLen = 78

// templateVarRE matches a {variable} placeholder (lowercase snake_case).
var templateVarRE = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// globalTemplateVars are substituted into every template (renderTemplate).
var globalTemplateVars = []string{
	"app_name", "app_url", "fqdn", "timestamp", "year",
	"onion_address", "onion_url", "i2p_address", "i2p_url", "notification_reply_to",
}

var (
	knownVarsOnce sync.Once
	knownVars     map[string]bool
)

// knownTemplateVars returns the global variables plus every variable used by an
// embedded default template (the defaults define each event's variable set).
func knownTemplateVars() map[string]bool {
	knownVarsOnce.Do(func() {
		knownVars = map[string]bool{}
		for _, v := range globalTemplateVars {
			knownVars[v] = true
		}
		entries, _ := fs.ReadDir(defaultTemplatesFS, "templates")
		for _, e := range entries {
			data, err := defaultTemplatesFS.ReadFile("templates/" + e.Name())
			if err != nil {
				continue
			}
			for _, m := range templateVarRE.FindAllStringSubmatch(string(data), -1) {
				knownVars[m[1]] = true
			}
		}
	})
	return knownVars
}

// ValidateTemplate checks a custom email template (PART 17 → Template
// Validation). errs are blocking; warnings are not.
func ValidateTemplate(text string) (errs, warnings []string) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Subject:") {
		return []string{"Invalid template syntax at line 1: expected \"Subject: ...\""}, nil
	}
	subject := strings.TrimSpace(strings.TrimPrefix(lines[0], "Subject:"))
	sep := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "---" {
			sep = i
			break
		}
	}
	if sep < 0 {
		return []string{fmt.Sprintf("Invalid template syntax at line %d: missing \"---\" separator", len(lines))}, nil
	}
	if subject == "" {
		errs = append(errs, "Subject cannot be empty")
	}
	if strings.TrimSpace(strings.Join(lines[sep+1:], "\n")) == "" {
		errs = append(errs, "Body cannot be empty")
	}
	if len(subject) > maxSubjectLen {
		warnings = append(warnings, fmt.Sprintf("Subject is %d characters; over %d may be truncated", len(subject), maxSubjectLen))
	}

	known := knownTemplateVars()
	seen := map[string]bool{}
	for _, m := range templateVarRE.FindAllStringSubmatch(text, -1) {
		v := m[1]
		if known[v] || seen[v] {
			continue
		}
		seen[v] = true
		msg := "Unknown variable: {" + v + "}"
		if hint := closestVar(v, known); hint != "" {
			msg += ". Did you mean {" + hint + "}?"
		}
		errs = append(errs, msg)
	}
	return errs, warnings
}

// closestVar returns the known variable within edit distance 2 of v, or "".
func closestVar(v string, known map[string]bool) string {
	names := make([]string, 0, len(known))
	for k := range known {
		names = append(names, k)
	}
	sort.Strings(names)
	best, bestD := "", 3
	for _, k := range names {
		if d := editDistance(v, k); d < bestD {
			best, bestD = k, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
