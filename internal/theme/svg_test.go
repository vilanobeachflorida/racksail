package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// svg wraps a drawing's insides in an SVG root.
func svg(inner string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10">` + inner + `</svg>`
}

// svgDrawings are files CheckSVG accepts; the fuzz test starts from them too.
var svgDrawings = map[string]string{
	"plain": logoSVG,
	"styled": svg(`<style>.a{fill:#01214F}@media (prefers-color-scheme:dark){.a{fill:#FFFFFF}}</style>` +
		`<defs><linearGradient id="g"/></defs><rect class="a" style="stroke: url(#g)" fill="url(#g)" width="10" height="10"/>` +
		`<text x="1" y="9">a\b</text>`),
	// As Illustrator saves them: a byte order mark, the XML declaration,
	// a document type the browser never fetches, xml:space and xlink.
	"exported": "\xef\xbb\xbf" + `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">` + "\n" +
		`<svg version="1.1" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" x="0px" y="0px" ` +
		`viewBox="0 0 10 10" style="enable-background:new 0 0 10 10;" xml:space="preserve">` +
		`<style type="text/css">` + "\n\t.st0{fill:url(#g);}\n" + `</style>` +
		`<linearGradient id="g" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="10" y2="10">` +
		`<stop offset="0" style="stop-color:#01214F"/><stop offset="1" style="stop-color:#007CFD"/></linearGradient>` +
		`<path class="st0" d="M0 0h10v10H0z"/><use xlink:href="#g"/></svg>` + "\n",
	// As Figma exports them: filters, clipping and presentation attributes.
	"filters": `<svg width="24" height="24" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">` +
		`<g clip-path="url(#clip)" filter="url(#shadow)"><path fill-rule="evenodd" clip-rule="evenodd" d="M2 2h20v20H2z" fill="#007CFD"/></g>` +
		`<defs><filter id="shadow" x="0" y="0" width="24" height="24" filterUnits="userSpaceOnUse" color-interpolation-filters="sRGB">` +
		`<feFlood flood-opacity="0" result="BackgroundImageFix"/>` +
		`<feColorMatrix in="SourceAlpha" type="matrix" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 127 0" result="hardAlpha"/>` +
		`<feOffset dy="1"/><feGaussianBlur stdDeviation="1"/><feBlend mode="normal" in2="BackgroundImageFix" result="effect"/></filter>` +
		`<clipPath id="clip"><rect width="24" height="24" fill="white"/></clipPath></defs></svg>`,
	// A prefix bound to the SVG namespace names SVG elements too.
	"prefixed": `<s:svg xmlns:s="http://www.w3.org/2000/svg"><s:rect width="1" height="1"/></s:svg>`,
	// Comments and CDATA sections in a style element aren't CSS.
	"style sections": svg(`<style><![CDATA[.a{fill:url(#g)}]]><!-- the brand blue -->.b{fill:#007CFD}</style>`),
	"accessible":     svg(`<title>Acme</title><desc>The Acme mark</desc><g role="img" aria-label="Acme" data-layer="1"/>`),
}

func TestCheckSVGAcceptsDrawings(t *testing.T) {
	for name, file := range svgDrawings {
		if err := CheckSVG([]byte(file)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Racksail's own logos and icons can go in a theme package too.
func TestCheckSVGAcceptsTheBrandFiles(t *testing.T) {
	files, err := filepath.Glob("../../brand/*/*.svg")
	if err != nil || len(files) == 0 {
		t.Fatalf("no brand files: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckSVG(data); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

const declareXHTML = `xmlns:h="http://www.w3.org/1999/xhtml"`

// svgRefusals are files CheckSVG refuses, with what the refusal says. The
// fuzz test starts from them too.
var svgRefusals = map[string]struct{ file, want string }{
	"not svg":      {`<html></html>`, "isn't an SVG image"},
	"empty":        {``, "isn't an SVG image"},
	"no namespace": {`<svg><rect/></svg>`, `add xmlns=`},
	"broken":       {svg(`<g>`), "can't be read"},
	"two roots":    {svg(``) + svg(`<rect/>`), "more than one root"},
	"text outside": {`x` + svg(``), "text outside"},

	// What runs code, links, embeds or animates.
	"script":         {svg(`<script>alert(1)</script>`), "<script>"},
	"handler":        {`<svg xmlns="http://www.w3.org/2000/svg" onload="x()"/>`, "event handlers"},
	"link":           {svg(`<a href="#x"><rect/></a>`), "<a>"},
	"foreign object": {svg(`<foreignObject/>`), "<foreignObject>"},
	"image":          {svg(`<image href="#x"/>`), "<image>"},
	"filter image":   {svg(`<filter id="f"><feImage href="#x"/></filter>`), "<feImage>"},
	"animation":      {svg(`<rect><animate attributeName="x" to="1"/></rect>`), "<animate>"},
	"set":            {svg(`<rect><set attributeName="fill" to="url(https://x.example/p.svg#p)"/></rect>`), "<set>"},
	"case":           {svg(`<Script>alert(1)</Script>`), "<Script>"},

	// Elements and attributes of other namespaces. Browsers run an XHTML
	// script and load an XHTML image's src inside an SVG document.
	"xhtml image":        {svg(`<img xmlns="http://www.w3.org/1999/xhtml" src="https://x.example/a.png"/>`), "another namespace"},
	"xhtml style":        {svg(`<h:style ` + declareXHTML + `>@import "https://x.example/a.css";</h:style>`), "another namespace"},
	"xhtml script":       {svg(`<h:script ` + declareXHTML + `>alert(1)</h:script>`), "another namespace"},
	"undeclared prefix":  {svg(`<p:rect/>`), "another namespace"},
	"unbound default":    {svg(`<g xmlns=""><rect/></g>`), "another namespace"},
	"editor elements":    {svg(`<sodipodi:namedview xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd"/>`), "plain SVG"},
	"src":                {svg(`<rect src="https://x.example/a.png"/>`), "src attribute"},
	"foreign attribute":  {svg(`<rect ` + declareXHTML + ` h:src="https://x.example/a.png"/>`), "other namespaces"},
	"svg-prefixed attr":  {svg(`<rect xmlns:s="http://www.w3.org/2000/svg" s:fill="red"/>`), "other namespaces"},
	"base":               {svg(`<g xml:base="https://x.example/"><use href="#a"/></g>`), "xml:base"},
	"xlink show":         {`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:show="new"/></svg>`, "xlink:show"},
	"duplicate":          {svg(`<rect fill="#000000" fill="url(https://x.example/p.svg#p)"/>`), "twice"},
	"duplicate expanded": {`<svg xmlns="http://www.w3.org/2000/svg" xmlns:a="http://www.w3.org/1999/xlink" xmlns:b="http://www.w3.org/1999/xlink"><use a:href="#a" b:href="#b"/></svg>`, "twice"},

	// References outside the file.
	"external link":   {`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="https://x.example/s.svg#a"/></svg>`, "own parts"},
	"empty link":      {svg(`<use href=""/>`), "own parts"},
	"spaced link":     {svg(`<use href="&#xA0;#a"/>`), "own parts"},
	"import":          {svg(`<style>@import "x.css";</style>`), "@import"},
	"escaped import":  {svg(`<style>@\69mport "https://x.example/a.css";</style>`), "@import"},
	"image-set":       {svg(`<style>rect{fill:image-set("https://x.example/a.png" 1x)}</style>`), "can't load"},
	"escaped url":     {svg(`<rect fill="u\72l(https://x.example/a.svg#p)"/>`), "can't load"},
	"attribute url":   {svg(`<rect style="filter: url('https://x.example/f.svg#f')"/>`), "can't load"},
	"referenced url":  {svg(`<rect fill="&#x75;rl(https://x.example/a.svg#p)"/>`), "can't load"},
	"stray closer":    {svg(`<rect fill="image-set( ] 'https://x.example/a.png' 1x)"/>`), "can't load"},
	"cdata import":    {svg(`<style><![CDATA[@import "x.css";]]></style>`), "@import"},
	"stylesheet":      {`<?xml-stylesheet href="x.css"?>` + svg(``), "style sheets"},
	"instruction":     {svg(`<?php echo 1 ?>`), "processing instructions"},
	"late xml":        {svg(``) + `<?xml version="1.0"?>`, "processing instructions"},
	"entity":          {`<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY a "aaa">]>` + svg(`&a;`), "entities"},
	"attribute list":  {`<!DOCTYPE svg [<!ATTLIST svg onload CDATA "alert(1)">]>` + svg(``), "attribute defaults"},
	"internal subset": {`<!DOCTYPE svg [<!-- nothing -->]>` + svg(``), "entities"},
	"late doctype":    {svg(`<!DOCTYPE svg>`), "document type"},

	// A style element's sheet is all of its text, however comments and
	// CDATA sections split it, as browsers read it.
	"split import":      {svg(`<style>@im<!-- -->port "https://x.example/a.css";</style>`), "@import"},
	"split url":         {svg(`<style>rect{fill:u<!-- -->rl(https://x.example/a.svg#p)}</style>`), "can't load"},
	"split cdata url":   {svg(`<style><![CDATA[rect{fill:ur]]>l(https://x.example/a.svg#p)}</style>`), "can't load"},
	"split image-set":   {svg(`<style>rect{fill:image-<!-- -->set("https://x.example/a.png" 1x)}</style>`), "can't load"},
	"split argument":    {svg(`<style>rect{fill:url(<![CDATA[https://x.example/a.svg#p]]>)}</style>`), "can't load"},
	"element in style":  {svg(`<style>rect{fill:u<g/>rl(https://x.example/a.svg#p)}</style>`), "only contain CSS"},
	"unclosed in parts": {svg(`<style>rect{fill:url(#a)<!-- --></style>`), "open"},

	// XML turns line breaks in attribute values into spaces, so a string
	// or url() broken by one in this reading is whole in a browser's.
	"newline in a string": {svg("<rect fill='\"\n\" url(https://x.example/a.svg#p)'/>"), "string"},
	"newline in a url":    {svg("<rect fill=\"url(https://x.example/a\\\nb.svg#p)\"/>"), "url()"},
}

func TestCheckSVGRefusals(t *testing.T) {
	for name, tt := range svgRefusals {
		err := CheckSVG([]byte(tt.file))
		if err == nil {
			t.Errorf("%s: should be refused", name)
		} else if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want it to mention %q", name, err, tt.want)
		}
	}
}
