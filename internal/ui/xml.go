package ui

import (
	"encoding/xml"
	"fmt"
	"hash/fnv"
	"strings"
)

// AndroidPackage is the package of the fake app.
const AndroidPackage = "com.zebrunner.dummy"

// ElementUUID is the stable Appium element id of an element.
func ElementUUID(id string) string {
	h := fnv.New64a()
	h.Write([]byte(id))
	v := h.Sum64()
	return fmt.Sprintf("00000000-0000-%04x-%04x-%012x", (v>>48)&0xffff, (v>>32)&0xffff, v&0xffffffffffff)
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// AndroidClass is the widget class of an element.
func AndroidClass(e *Element) string {
	switch e.Kind {
	case KindLabel:
		return "android.widget.TextView"
	case KindButton:
		return "android.widget.Button"
	case KindInput:
		return "android.widget.EditText"
	}
	return "android.widget.FrameLayout"
}

// AndroidResourceID is the resource-id of an element.
func AndroidResourceID(e *Element) string { return AndroidPackage + ":id/" + e.ID }

// AndroidBounds is the bounds attribute of an element.
func AndroidBounds(r Rect) string {
	return fmt.Sprintf("[%d,%d][%d,%d]", r.X, r.Y, r.X+r.W, r.Y+r.H)
}

// AndroidAttr is an attribute of an element as UiAutomator2 reports it; ok is false for an unknown attribute.
func AndroidAttr(e *Element, name string) (string, bool) {
	clickable := e.Kind == KindButton || e.Kind == KindInput
	switch name {
	case "text", "name":
		return e.Text, true
	case "resource-id", "resourceId":
		return AndroidResourceID(e), true
	case "content-desc", "contentDescription":
		if e.Kind == KindContainer {
			return "", true
		}
		return e.ID, true
	case "class", "className":
		return AndroidClass(e), true
	case "package":
		return AndroidPackage, true
	case "bounds":
		return AndroidBounds(e.Rect), true
	case "clickable", "focusable", "long-clickable":
		return fmt.Sprint(clickable), true
	case "focused":
		return fmt.Sprint(e.Focused), true
	case "enabled", "displayed":
		return "true", true
	case "checkable", "checked", "password", "scrollable", "selected":
		return "false", true
	}
	return "", false
}

// AndroidXML is the page source of UiAutomator2 (dumpWindowHierarchy).
func AndroidXML(root *Element, rotation int) string {
	var b strings.Builder
	b.WriteString(`<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>`)
	fmt.Fprintf(&b, `<hierarchy index="0" class="hierarchy" rotation="%d" width="%d" height="%d">`, rotation/90, root.Rect.W, root.Rect.H)
	var walk func(e *Element, index int)
	walk = func(e *Element, index int) {
		cls := AndroidClass(e)
		fmt.Fprintf(&b, `<%s index="%d"`, cls, index)
		for _, a := range []string{"package", "class", "text", "resource-id", "checkable", "checked", "clickable", "enabled",
			"focusable", "focused", "long-clickable", "password", "scrollable", "selected", "bounds", "displayed"} {
			v, _ := AndroidAttr(e, a)
			fmt.Fprintf(&b, ` %s="%s"`, a, esc(v))
		}
		desc, _ := AndroidAttr(e, "content-desc")
		fmt.Fprintf(&b, ` content-desc="%s"`, esc(desc))
		if len(e.Children) == 0 {
			b.WriteString(" />")
			return
		}
		b.WriteString(">")
		for i, c := range e.Children {
			walk(c, i)
		}
		fmt.Fprintf(&b, "</%s>", cls)
	}
	walk(root, 0)
	b.WriteString("</hierarchy>")
	return b.String()
}

// IOSType is the XCUIElementType of an element.
func IOSType(e *Element) string {
	switch e.Kind {
	case KindLabel:
		return "XCUIElementTypeStaticText"
	case KindButton:
		return "XCUIElementTypeButton"
	case KindInput:
		return "XCUIElementTypeTextField"
	}
	return "XCUIElementTypeOther"
}

// IOSAttr is an attribute of an element as WebDriverAgent reports it.
func IOSAttr(e *Element, name string) (string, bool) {
	switch name {
	case "type":
		return IOSType(e), true
	case "name", "identifier":
		return e.ID, true
	case "label":
		if e.Kind == KindInput {
			return "", true
		}
		return e.Text, true
	case "value":
		if e.Kind == KindButton || e.Kind == KindContainer {
			return "", true
		}
		return e.Text, true
	case "enabled", "visible", "hittable":
		return "true", true
	case "accessible":
		return fmt.Sprint(e.Kind != KindContainer), true
	case "selected", "focused":
		return fmt.Sprint(e.Focused), true
	case "x":
		return fmt.Sprint(e.Rect.X), true
	case "y":
		return fmt.Sprint(e.Rect.Y), true
	case "width":
		return fmt.Sprint(e.Rect.W), true
	case "height":
		return fmt.Sprint(e.Rect.H), true
	}
	return "", false
}

// IOSXML is the page source of WebDriverAgent.
func IOSXML(root *Element, appName string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	r := root.Rect
	fmt.Fprintf(&b, `<XCUIElementTypeApplication type="XCUIElementTypeApplication" name="%s" label="%s" enabled="true" visible="true" accessible="false" x="0" y="0" width="%d" height="%d" index="0">`,
		esc(appName), esc(appName), r.W, r.H)
	fmt.Fprintf(&b, `<XCUIElementTypeWindow type="XCUIElementTypeWindow" enabled="true" visible="true" accessible="false" x="0" y="0" width="%d" height="%d" index="0">`, r.W, r.H)
	var walk func(e *Element, index int)
	walk = func(e *Element, index int) {
		t := IOSType(e)
		fmt.Fprintf(&b, `<%s`, t)
		for _, a := range []string{"type", "value", "name", "label", "enabled", "visible", "accessible", "x", "y", "width", "height"} {
			v, _ := IOSAttr(e, a)
			if (a == "value" || a == "label") && v == "" {
				continue
			}
			fmt.Fprintf(&b, ` %s="%s"`, a, esc(v))
		}
		fmt.Fprintf(&b, ` index="%d"`, index)
		if len(e.Children) == 0 {
			b.WriteString("/>")
			return
		}
		b.WriteString(">")
		for i, c := range e.Children {
			walk(c, i)
		}
		fmt.Fprintf(&b, "</%s>", t)
	}
	walk(root, 0)
	b.WriteString(`</XCUIElementTypeWindow></XCUIElementTypeApplication>`)
	return b.String()
}

// uia2Attributes are the attributes the real UiAutomator2 server exposes (Attribute.java of 9.10.5), with their
// aliases: getAttribute of another name is `unknown command`.
var uia2Attributes = [][]string{
	{"checkable"}, {"checked"}, {"class", "className"}, {"clickable"}, {"content-desc", "contentDescription"},
	{"enabled"}, {"focusable"}, {"focused"}, {"long-clickable", "longClickable"}, {"package"}, {"password"},
	{"resource-id", "resourceId"}, {"scrollable"}, {"selection-start"}, {"selection-end"}, {"selected"},
	{"text", "name"}, {"hint"}, {"extras"}, {"bounds"}, {"displayed"}, {"contentSize"},
	{"a11y-important", "importantForAccessibility"}, {"screen-reader-focusable", "screenReaderFocusable"},
	{"input-type", "inputType"}, {"drawing-order", "drawingOrder"}, {"showing-hint", "showingHintText"},
	{"text-entry-key", "textEntryKey"}, {"multiline", "multiLine"}, {"dismissable"},
	{"a11y-focused", "accessibilityFocused"}, {"heading"}, {"live-region", "liveRegion"},
	{"context-clickable", "contextClickable"}, {"max-text-length", "maxTextLength"},
	{"content-invalid", "contentInvalid"}, {"error", "errorText"}, {"pane-title", "paneTitle"},
	{"tooltip-text", "tooltipText"}, {"text-has-clickable-span", "textHasClickableSpan"}, {"actions"},
	{"window-id", "windowId"}, {"text-size", "textSize"}, {"text-unit", "textUnit"},
}

// UIA2AttrNames is the list of the error message of an unknown attribute, as the real server writes it.
func UIA2AttrNames() string {
	parts := make([]string, 0, len(uia2Attributes))
	for _, a := range uia2Attributes {
		if len(a) == 1 {
			parts = append(parts, a[0])
		} else {
			parts = append(parts, "{"+strings.Join(a, ",")+"}")
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// UIA2Attr is an attribute of an element as the real UiAutomator2 server answers getAttribute: known is false for
// a name it does not expose, null for an attribute without a value.
func UIA2Attr(e *Element, name string) (value string, known, null bool) {
	canonical := ""
	for _, a := range uia2Attributes {
		for _, alias := range a {
			if alias == name {
				canonical = a[0]
			}
		}
	}
	if canonical == "" {
		return "", false, false
	}
	switch canonical {
	case "content-desc":
		if e.Kind == KindContainer {
			return "", true, true
		}
		return e.ID, true, false
	case "text":
		return e.Text, true, false
	case "contentSize", "selection-start", "selection-end", "hint", "extras", "error", "pane-title", "tooltip-text",
		"actions", "text-size", "text-unit", "max-text-length":
		return "", true, true
	case "input-type", "drawing-order", "window-id", "live-region":
		return "0", true, false
	case "a11y-important", "screen-reader-focusable", "text-entry-key", "multiline", "dismissable", "a11y-focused",
		"heading", "context-clickable", "content-invalid", "showing-hint", "text-has-clickable-span":
		return fmt.Sprint(canonical == "a11y-important"), true, false
	}
	v, ok := AndroidAttr(e, canonical)
	return v, ok, false
}
