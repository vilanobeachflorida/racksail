package theme

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The namespaces of SVG elements and of the few attributes from elsewhere
// that SVG files may use.
const (
	svgNamespace   = "http://www.w3.org/2000/svg"
	xlinkNamespace = "http://www.w3.org/1999/xlink"
	xmlNamespace   = "http://www.w3.org/XML/1998/namespace"
)

// svgElements are the elements SVG files in themes may contain: structure,
// shapes, text, paint servers, clipping, masking and filters. Nothing that
// runs code, links somewhere, embeds a document or an image, animates or
// brings a font. XML names are case-sensitive, and so is this list.
var svgElements = map[string]bool{
	"svg": true, "g": true, "defs": true, "symbol": true, "use": true,
	"title": true, "desc": true, "metadata": true, "style": true,
	"path": true, "rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true,
	"text": true, "tspan": true, "textPath": true,
	"linearGradient": true, "radialGradient": true, "stop": true, "pattern": true,
	"clipPath": true, "mask": true, "marker": true,
	"filter": true, "feBlend": true, "feColorMatrix": true, "feComponentTransfer": true, "feComposite": true,
	"feConvolveMatrix": true, "feDiffuseLighting": true, "feDisplacementMap": true, "feDistantLight": true,
	"feDropShadow": true, "feFlood": true, "feFuncA": true, "feFuncB": true, "feFuncG": true, "feFuncR": true,
	"feGaussianBlur": true, "feMerge": true, "feMergeNode": true, "feMorphology": true, "feOffset": true,
	"fePointLight": true, "feSpecularLighting": true, "feSpotLight": true, "feTile": true, "feTurbulence": true,
}

// svgAttributes are the attributes, in no namespace, that those elements
// may have: geometry, the settings of paint servers, markers and filters,
// the presentation attributes browsers read as CSS, and a few for
// accessibility. None of them loads anything or runs code, except href,
// which may only point to a part of the same file. Attributes starting
// with aria- and data- are allowed too.
var svgAttributes = map[string]bool{
	// Every element.
	"id": true, "class": true, "style": true, "lang": true, "tabindex": true, "role": true, "focusable": true,
	// Structure and geometry.
	"version": true, "baseProfile": true, "viewBox": true, "preserveAspectRatio": true, "zoomAndPan": true,
	"x": true, "y": true, "width": true, "height": true, "transform": true, "href": true,
	"d": true, "pathLength": true, "points": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true,
	"x1": true, "y1": true, "x2": true, "y2": true,
	// Text.
	"dx": true, "dy": true, "rotate": true, "textLength": true, "lengthAdjust": true,
	"startOffset": true, "method": true, "spacing": true, "side": true, "path": true,
	// Markers and symbols.
	"refX": true, "refY": true, "markerUnits": true, "markerWidth": true, "markerHeight": true, "orient": true,
	// Gradients, patterns, clipping and masks.
	"gradientUnits": true, "gradientTransform": true, "spreadMethod": true, "fx": true, "fy": true, "fr": true,
	"offset": true, "patternUnits": true, "patternContentUnits": true, "patternTransform": true,
	"clipPathUnits": true, "maskUnits": true, "maskContentUnits": true,
	// Filters.
	"filterUnits": true, "primitiveUnits": true, "in": true, "in2": true, "result": true, "mode": true,
	"type": true, "values": true, "tableValues": true, "slope": true, "intercept": true, "amplitude": true,
	"exponent": true, "operator": true, "k1": true, "k2": true, "k3": true, "k4": true, "order": true,
	"kernelMatrix": true, "divisor": true, "bias": true, "targetX": true, "targetY": true, "edgeMode": true,
	"kernelUnitLength": true, "preserveAlpha": true, "surfaceScale": true, "diffuseConstant": true,
	"specularConstant": true, "specularExponent": true, "scale": true, "xChannelSelector": true,
	"yChannelSelector": true, "azimuth": true, "elevation": true, "z": true, "pointsAtX": true,
	"pointsAtY": true, "pointsAtZ": true, "limitingConeAngle": true, "stdDeviation": true, "radius": true,
	"baseFrequency": true, "numOctaves": true, "seed": true, "stitchTiles": true,
	// Style elements.
	"media": true, "title": true,
	// Presentation attributes.
	"alignment-baseline": true, "baseline-shift": true, "clip": true, "clip-path": true, "clip-rule": true,
	"color": true, "color-interpolation": true, "color-interpolation-filters": true, "color-rendering": true,
	"direction": true, "display": true, "dominant-baseline": true, "enable-background": true, "fill": true,
	"fill-opacity": true, "fill-rule": true, "filter": true, "flood-color": true, "flood-opacity": true,
	"font": true, "font-family": true, "font-feature-settings": true, "font-kerning": true, "font-size": true,
	"font-size-adjust": true, "font-stretch": true, "font-style": true, "font-variant": true, "font-weight": true,
	"glyph-orientation-horizontal": true, "glyph-orientation-vertical": true, "image-rendering": true,
	"isolation": true, "kerning": true, "letter-spacing": true, "lighting-color": true, "marker": true,
	"marker-end": true, "marker-mid": true, "marker-start": true, "mask": true, "mask-type": true,
	"mix-blend-mode": true, "opacity": true, "overflow": true, "paint-order": true, "pointer-events": true,
	"shape-rendering": true, "stop-color": true, "stop-opacity": true, "stroke": true, "stroke-dasharray": true,
	"stroke-dashoffset": true, "stroke-linecap": true, "stroke-linejoin": true, "stroke-miterlimit": true,
	"stroke-opacity": true, "stroke-width": true, "text-anchor": true, "text-decoration": true,
	"text-overflow": true, "text-rendering": true, "transform-origin": true, "unicode-bidi": true,
	"vector-effect": true, "visibility": true, "white-space": true, "word-spacing": true, "writing-mode": true,
}

// CheckSVG makes sure an SVG image is only a drawing: SVG's drawing
// elements and the attributes that place and style them, and nothing that
// runs code, links, embeds a document or an image, animates or refers to
// anything outside the file. Scripts, event handlers, entity declarations,
// processing instructions, and elements and attributes of other
// namespaces, such as an editor's own, are refused. The file is read as
// browsers read XML, and its styles, in style elements, style attributes
// and the attributes browsers read as CSS, as browsers read CSS, so
// escapes, image functions, comments and character references can't hide
// a reference.
func CheckSVG(data []byte) error {
	upper := bytes.ToUpper(data)
	if bytes.Contains(upper, []byte("<!ENTITY")) || bytes.Contains(upper, []byte("<!ATTLIST")) {
		return errors.New("SVG files can't declare entities or attribute defaults")
	}
	// Browsers skip a byte order mark; the decoder would read it as text.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	var (
		first   = true
		started bool
		depth   int
		// style collects the text of the style element being read: the
		// sheet is all of it, however comments and CDATA sections split it.
		style *strings.Builder
	)
	for ; ; first = false {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("the SVG can't be read: %w", err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch {
			case started && depth == 0:
				return errors.New("the file has more than one root element; an SVG image is one <svg> element")
			case style != nil:
				return fmt.Errorf("SVG style elements can only contain CSS, not <%s> elements", el.Name.Local)
			case !started && el.Name.Local == "svg" && el.Name.Space == "":
				return errors.New(`the SVG doesn't say it's SVG; add xmlns="http://www.w3.org/2000/svg" to its <svg> element`)
			case !started && (el.Name.Local != "svg" || el.Name.Space != svgNamespace):
				return errors.New("the file isn't an SVG image")
			}
			started = true
			if err := checkSVGElement(el); err != nil {
				return err
			}
			depth++
			if el.Name.Local == "style" {
				style = &strings.Builder{}
			}
		case xml.EndElement:
			depth--
			if style != nil {
				// Elements inside style are refused, so this ends it.
				if err := svgCSSRules.checkSheet(style.String()); err != nil {
					return err
				}
				style = nil
			}
		case xml.CharData:
			switch {
			case style != nil:
				style.Write(el)
			case depth == 0 && strings.Trim(string(el), " \t\r\n") != "":
				return errors.New("the file has text outside its <svg> element")
			}
		case xml.ProcInst:
			switch {
			case first && el.Target == "xml":
				// The XML declaration, whose version and encoding the decoder checks.
			case strings.EqualFold(el.Target, "xml-stylesheet"):
				return errors.New("SVG images can't load style sheets")
			default:
				return fmt.Errorf("SVG images can't contain processing instructions such as <?%s?>", el.Target)
			}
		case xml.Directive:
			if err := checkSVGDirective(string(el), started); err != nil {
				return err
			}
		}
	}
	if !started {
		return errors.New("the file isn't an SVG image")
	}
	return nil
}

// checkSVGDirective allows one thing that starts with <!: a document type
// declaration before the drawing, without declarations of its own.
// Browsers don't fetch the DTD it names.
func checkSVGDirective(d string, started bool) error {
	fields := strings.Fields(d)
	switch {
	case len(fields) == 0 || fields[0] != "DOCTYPE":
		return errors.New("the SVG can't be read: it has a declaration that isn't its document type")
	case started:
		return errors.New("the SVG can't be read: its document type declaration comes after the drawing starts")
	case strings.Contains(d, "["):
		return errors.New("SVG files can't declare entities or attribute defaults")
	}
	return nil
}

func checkSVGElement(el xml.StartElement) error {
	name := el.Name.Local
	if el.Name.Space != svgNamespace {
		return fmt.Errorf("SVG images can only contain SVG drawing elements, and <%s> is from another namespace; save the file as a plain SVG", name)
	}
	if !svgElements[name] {
		return fmt.Errorf("SVG images can't contain <%s> elements", name)
	}
	seen := make(map[xml.Name]bool, len(el.Attr))
	for _, a := range el.Attr {
		if seen[a.Name] {
			return fmt.Errorf("the SVG can't be read: <%s> has the %s attribute twice", name, a.Name.Local)
		}
		seen[a.Name] = true
		if err := checkSVGAttribute(name, a); err != nil {
			return err
		}
	}
	return nil
}

func checkSVGAttribute(element string, a xml.Attr) error {
	name := a.Name.Local
	switch a.Name.Space {
	case "":
		switch {
		case name == "xmlns":
			// Declares the namespace of the element and those inside it.
		case strings.HasPrefix(strings.ToLower(name), "on"):
			return fmt.Errorf("SVG images can't have event handlers such as %s", name)
		case name == "href":
			return checkSVGLink(a.Value)
		case name == "style":
			return svgCSSRules.checkStyle(a.Value)
		case !svgAttributes[name] && !strings.HasPrefix(name, "aria-") && !strings.HasPrefix(name, "data-"):
			return fmt.Errorf("SVG images can't use the %s attribute on <%s>", name, element)
		}
	case "xmlns":
		// Declares a namespace prefix; what uses it is checked.
	case xlinkNamespace:
		if name != "href" {
			return fmt.Errorf("SVG images can't use the xlink:%s attribute", name)
		}
		return checkSVGLink(a.Value)
	case xmlNamespace:
		if name != "lang" && name != "space" {
			return fmt.Errorf("SVG images can't use the xml:%s attribute", name)
		}
	default:
		return fmt.Errorf("SVG images can't use attributes from other namespaces, such as %s on <%s>; save the file as a plain SVG", name, element)
	}
	// Browsers read presentation attributes as CSS, and any attribute may
	// one day be one, so every value is checked for references.
	return svgCSSRules.checkValue(a.Value)
}

// checkSVGLink allows links to parts of the same file only, read as the
// URL parser reads them.
func checkSVGLink(href string) error {
	if !hasURLPrefix(href, "#") {
		return fmt.Errorf("SVG images can only link to their own parts, not %q", href)
	}
	return nil
}
