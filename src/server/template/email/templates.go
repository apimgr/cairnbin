// Package emailtemplates contains the built-in notification email templates.
package emailtemplates

import "embed"

// FS embeds the default email templates for the mailer.
//go:embed templates/*.txt
var FS embed.FS
