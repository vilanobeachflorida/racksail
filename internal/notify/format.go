package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/theme"
)

// severityLabel is how a message says how urgent it is.
var severityLabel = map[events.Severity]string{
	events.SeverityInfo: "Info", events.SeverityNotice: "Notice", events.SeverityWarning: "Warning",
	events.SeverityError: "Error", events.SeverityCritical: "Critical",
}

// Subject is a message's title, prefixed with its severity when it needs
// attention.
func Subject(msg Message) string {
	if msg.Severity.AtLeast(events.SeverityWarning) {
		return severityLabel[msg.Severity] + ": " + msg.Title
	}
	return msg.Title
}

// Text is a message as plain text: its body, the messages a digest holds,
// and the link back to the panel.
func Text(msg Message) string {
	var b strings.Builder
	if body := strings.TrimSpace(msg.Body); body != "" {
		b.WriteString(body)
	}
	for _, m := range msg.Digest {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%s\n%s", Subject(m), strings.TrimSpace(m.Body))
		if m.URL != "" {
			fmt.Fprintf(&b, "\n%s", m.URL)
		}
	}
	if msg.URL != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		label := msg.ActionLabel
		if label == "" {
			label = "Open in the panel"
		}
		fmt.Fprintf(&b, "%s: %s", label, msg.URL)
	}
	return b.String()
}

// HTML renders a message with the theme's email template, which escapes
// every value for where it stands.
func HTML(ctx context.Context, msg Message, env Env) (string, error) {
	pkg := &theme.Package{Theme: theme.Default()}
	if env.Theme != nil {
		p, err := env.Theme(ctx)
		if err != nil {
			return "", err
		}
		if p != nil {
			pkg = p
		}
	}
	brand := env.BrandName
	if brand == "" {
		brand = "Racksail"
	}
	body := strings.TrimSpace(msg.Body)
	for _, m := range msg.Digest {
		if body != "" {
			body += "\n\n"
		}
		body += Subject(m) + "\n" + strings.TrimSpace(m.Body)
	}
	label := msg.ActionLabel
	if label == "" && msg.URL != "" {
		label = "Open in the panel"
	}
	return pkg.Render(theme.TemplateEmail, map[string]string{
		"lang": "en", "dir": "ltr", "brandName": brand, "title": Subject(msg), "preheader": firstLine(body), "body": body,
		"actionLabel": label, "actionUrl": msg.URL,
		"footer": "You get this because of your notification settings in " + brand + ".",
	})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 140 {
		s = s[:140]
	}
	return s
}

// priority maps a severity to ntfy's five priorities.
func priority(s events.Severity) int {
	switch s {
	case events.SeverityCritical:
		return 5
	case events.SeverityError:
		return 4
	case events.SeverityWarning:
		return 3
	case events.SeverityNotice:
		return 2
	}
	return 1
}

// color is a severity's color in chat embeds, as RGB.
func color(s events.Severity) int {
	switch s {
	case events.SeverityCritical, events.SeverityError:
		return 0xD1242F
	case events.SeverityWarning:
		return 0xBF8700
	case events.SeverityNotice:
		return 0x007CFD
	}
	return 0x1A7F37
}
