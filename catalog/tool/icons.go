package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image/png"
	"io"
	"strings"
)

// maxIconPixels bounds a PNG icon's sides; icons are shown small.
const maxIconPixels = 1024

// checkSVG refuses icons that could run code or load anything when a panel
// shows them: scripts, event handlers, embedded documents, declarations,
// references to anything outside the file, and animations that would set a
// link or a handler, which an icon shown inline would follow.
func checkSVG(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	seenRoot := false
	inStyle := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("isn't valid SVG: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if !seenRoot {
				if name != "svg" {
					return errors.New("isn't an SVG file: the first element must be <svg>")
				}
				seenRoot = true
			}
			switch name {
			case "script", "handler", "foreignobject", "iframe", "embed", "object", "audio", "video":
				return fmt.Errorf("contains a <%s> element, which icons can't use", t.Name.Local)
			case "style":
				inStyle++
			}
			for _, a := range t.Attr {
				attr := strings.ToLower(a.Name.Local)
				switch {
				case strings.HasPrefix(attr, "on"):
					return fmt.Errorf("has an event handler (%s), which icons can't use", a.Name.Local)
				case attr == "href" && !strings.HasPrefix(strings.TrimSpace(a.Value), "#"):
					return fmt.Errorf("refers to %q; icons can only refer to their own elements", a.Value)
				case attr == "attributename" && animatesLinkOrHandler(a.Value):
					// The values an animation sets never pass the href check.
					return fmt.Errorf("has a <%s> that animates %s, which icons can't do", t.Name.Local, a.Value)
				}
				if err := checkCSS(a.Value); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, "style") && inStyle > 0 {
				inStyle--
			}
		case xml.CharData:
			if inStyle > 0 {
				if err := checkCSS(string(t)); err != nil {
					return err
				}
			}
		case xml.Directive:
			return errors.New("has a declaration such as <!DOCTYPE>, which icons can't use")
		case xml.ProcInst:
			if t.Target != "xml" {
				return fmt.Errorf("has a <?%s?> processing instruction, which icons can't use", t.Target)
			}
		}
	}
	if !seenRoot {
		return errors.New("is empty")
	}
	return nil
}

// animatesLinkOrHandler reports whether an animation's attributeName is a
// link, with or without a namespace prefix, or an event handler.
func animatesLinkOrHandler(name string) bool {
	target := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(target, ":"); i >= 0 {
		target = target[i+1:]
	}
	return target == "href" || strings.HasPrefix(target, "on")
}

// checkCSS refuses style that loads anything: url() other than to an element
// of the same file, and @import.
func checkCSS(s string) error {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "@import") {
		return errors.New("imports a style sheet, which icons can't do")
	}
	for rest := lower; ; {
		i := strings.Index(rest, "url(")
		if i < 0 {
			return nil
		}
		rest = strings.TrimLeft(rest[i+len("url("):], " \t\r\n'\"")
		if !strings.HasPrefix(rest, "#") {
			return errors.New("loads something with url(); icons can only refer to their own elements")
		}
	}
}

func checkPNG(data []byte) error {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("isn't a valid PNG file: %w", err)
	}
	if cfg.Width > maxIconPixels || cfg.Height > maxIconPixels {
		return fmt.Errorf("is %dx%d pixels; icons are at most %dx%d", cfg.Width, cfg.Height, maxIconPixels, maxIconPixels)
	}
	return nil
}
