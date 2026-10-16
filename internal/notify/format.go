package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/theme"
)

// severityLabel is how a message says how urgent it is, in its language.
func severityLabel(locale string, s events.Severity) string {
	if !s.Valid() {
		s = events.SeverityInfo
	}
	return words(locale, "notify.severity."+string(s), nil)
}

// Subject is a message's title, prefixed with its severity when it needs
// attention.
func Subject(msg Message) string {
	if msg.Severity.AtLeast(events.SeverityWarning) {
		return words(msg.Locale, "notify.subject", map[string]any{"severity": severityLabel(msg.Locale, msg.Severity), "title": msg.Title})
	}
	return msg.Title
}

// actionLabel names a message's link back to the panel.
func actionLabel(msg Message) string {
	if msg.ActionLabel != "" {
		return msg.ActionLabel
	}
	return words(msg.Locale, "notify.action.open", nil)
}

// Text is a message as plain text: its body, the messages a digest holds,
// and the link back to the panel.
func Text(msg Message) string {
	var b strings.Builder
	if body := strings.TrimSpace(msg.Body); body != "" {
		b.WriteString(body)
	}
	for _, m := range msg.Digest {
		if m.Locale == "" {
			m.Locale = msg.Locale
		}
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
		b.WriteString(words(msg.Locale, "notify.link", map[string]any{"label": actionLabel(msg), "url": msg.URL}))
	}
	return b.String()
}

// themeOf is the theme messages are dressed in: the panel's, or the
// default one.
func themeOf(ctx context.Context, env Env) (*theme.Package, error) {
	pkg := &theme.Package{Theme: theme.Default()}
	if env.Theme != nil {
		p, err := env.Theme(ctx)
		if err != nil {
			return nil, err
		}
		if p != nil {
			pkg = p
		}
	}
	return pkg, nil
}

// HTML renders a message with the theme's email template, which escapes
// every value for where it stands.
func HTML(ctx context.Context, msg Message, env Env) (string, error) {
	pkg, err := themeOf(ctx, env)
	if err != nil {
		return "", err
	}
	brand := env.brand()
	body := strings.TrimSpace(msg.Body)
	for _, m := range msg.Digest {
		if m.Locale == "" {
			m.Locale = msg.Locale
		}
		if body != "" {
			body += "\n\n"
		}
		body += Subject(m) + "\n" + strings.TrimSpace(m.Body)
	}
	label := ""
	if msg.URL != "" {
		label = actionLabel(msg)
	}
	return pkg.Render(theme.TemplateEmail, map[string]string{
		"lang": language(msg.Locale), "dir": direction(msg.Locale), "brandName": brand, "title": Subject(msg), "preheader": firstLine(body),
		"body": body, "actionLabel": label, "actionUrl": msg.URL,
		"footer": words(msg.Locale, "notify.footer", map[string]any{"brand": brand}),
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

// statusColor is a severity's status color in the theme, as chat embeds
// take it: an RGB number. Chat apps show embeds on a dark background by
// default, so the dark palette's colors are used.
func statusColor(ctx context.Context, env Env, s events.Severity) (int, error) {
	pkg, err := themeOf(ctx, env)
	if err != nil {
		return 0, err
	}
	p := pkg.Theme.Tokens.Dark
	c := p.Success
	switch s {
	case events.SeverityCritical, events.SeverityError:
		c = p.Danger
	case events.SeverityWarning:
		c = p.Warning
	case events.SeverityNotice:
		c = p.Info
	}
	rgb, err := c.Parse()
	if err != nil {
		return 0, err
	}
	return int(rgb.R)<<16 | int(rgb.G)<<8 | int(rgb.B), nil
}
