package ui

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/antchfx/xmlquery"
)

// ErrInvalidSelector is returned for a locator that can't be parsed.
var ErrInvalidSelector = errors.New("invalid selector")

// FindAndroid finds elements of the layout with a UiAutomator2 locator strategy.
func FindAndroid(root *Element, rotation int, using, value string) ([]*Element, error) {
	all := Elements(root)
	match := func(f func(*Element) bool) []*Element {
		var out []*Element
		for _, e := range all {
			if f(e) {
				out = append(out, e)
			}
		}
		return out
	}
	switch using {
	case "id":
		short := value
		if i := strings.LastIndex(value, ":id/"); i >= 0 {
			short = value[i+4:]
		}
		return match(func(e *Element) bool { return e.ID == short }), nil
	case "accessibility id":
		return match(func(e *Element) bool { d, _ := AndroidAttr(e, "content-desc"); return d == value }), nil
	case "class name":
		return match(func(e *Element) bool { return AndroidClass(e) == value }), nil
	case "xpath":
		return findXPath(AndroidXML(root, rotation), value, all, func(n *xmlquery.Node) string {
			id := n.SelectAttr("resource-id")
			if i := strings.LastIndex(id, ":id/"); i >= 0 {
				return id[i+4:]
			}
			return ""
		})
	case "-android uiautomator":
		conds, err := parseUiSelector(value)
		if err != nil {
			return nil, err
		}
		return match(func(e *Element) bool {
			for _, c := range conds {
				if !c(e) {
					return false
				}
			}
			return true
		}), nil
	}
	return nil, fmt.Errorf("%w: unsupported locator strategy %q", ErrInvalidSelector, using)
}

var uiSelectorCall = regexp.MustCompile(`\.(\w+)\(\s*(?:"((?:[^"\\]|\\.)*)"|(true|false|\d+))?\s*\)`)

func parseUiSelector(s string) ([]func(*Element) bool, error) {
	calls := uiSelectorCall.FindAllStringSubmatch(s, -1)
	if len(calls) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidSelector, s)
	}
	var conds []func(*Element) bool
	for _, c := range calls {
		method, arg := c[1], c[2]
		attr := func(name string) func(*Element) string {
			return func(e *Element) string { v, _ := AndroidAttr(e, name); return v }
		}
		var get func(*Element) string
		var cmp func(v string) bool
		switch method {
		case "resourceId":
			get, cmp = attr("resource-id"), func(v string) bool { return v == arg }
		case "resourceIdMatches":
			re, err := regexp.Compile(arg)
			if err != nil {
				return nil, err
			}
			get, cmp = attr("resource-id"), re.MatchString
		case "text":
			get, cmp = attr("text"), func(v string) bool { return v == arg }
		case "textContains":
			get, cmp = attr("text"), func(v string) bool { return strings.Contains(v, arg) }
		case "textStartsWith":
			get, cmp = attr("text"), func(v string) bool { return strings.HasPrefix(v, arg) }
		case "textMatches":
			re, err := regexp.Compile(arg)
			if err != nil {
				return nil, err
			}
			get, cmp = attr("text"), re.MatchString
		case "description":
			get, cmp = attr("content-desc"), func(v string) bool { return v == arg }
		case "descriptionContains":
			get, cmp = attr("content-desc"), func(v string) bool { return strings.Contains(v, arg) }
		case "className":
			get, cmp = attr("class"), func(v string) bool { return v == arg }
		default:
			continue // clickable(true), instance(0), scrollable... are ignored
		}
		g, m := get, cmp
		conds = append(conds, func(e *Element) bool { return m(g(e)) })
	}
	return conds, nil
}

// FindIOS finds elements of the layout with a XCUITest locator strategy.
func FindIOS(root *Element, appName, using, value string) ([]*Element, error) {
	all := Elements(root)
	match := func(f func(*Element) bool) []*Element {
		var out []*Element
		for _, e := range all {
			if e.Kind != KindContainer && f(e) {
				out = append(out, e)
			}
		}
		return out
	}
	switch using {
	case "accessibility id", "id", "name", "link text":
		return match(func(e *Element) bool {
			return e.ID == value || (using == "link text" && e.Text == value)
		}), nil
	case "class name":
		return match(func(e *Element) bool { return IOSType(e) == value }), nil
	case "xpath":
		return findXPath(IOSXML(root, appName), value, all, func(n *xmlquery.Node) string { return n.SelectAttr("name") })
	case "-ios predicate string":
		pred, err := parsePredicate(value)
		if err != nil {
			return nil, err
		}
		return match(pred), nil
	case "-ios class chain":
		return findClassChain(all, value)
	}
	return nil, fmt.Errorf("%w: unsupported locator strategy %q", ErrInvalidSelector, using)
}

func findXPath(doc, expr string, all []*Element, key func(*xmlquery.Node) string) ([]*Element, error) {
	root, err := xmlquery.Parse(strings.NewReader(doc))
	if err != nil {
		return nil, err
	}
	nodes, err := xmlquery.QueryAll(root, expr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSelector, err)
	}
	byID := map[string]*Element{}
	for _, e := range all {
		byID[e.ID] = e
	}
	var out []*Element
	seen := map[*Element]bool{}
	for _, n := range nodes {
		e := byID[key(n)]
		if e == nil {
			e = all[0]
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out, nil
}

var predicateCond = regexp.MustCompile(`(?i)^\s*(\w+)\s*(==|=|!=|CONTAINS(?:\[c\])?|BEGINSWITH(?:\[c\])?|ENDSWITH(?:\[c\])?|LIKE)\s*(?:'([^']*)'|"([^"]*)"|(\d+|true|false|TRUE|FALSE|YES|NO))\s*$`)

func parsePredicate(s string) (func(*Element) bool, error) {
	var groups [][]func(*Element) bool
	for _, orPart := range regexp.MustCompile(`(?i)\s+OR\s+|\s*\|\|\s*`).Split(s, -1) {
		var conds []func(*Element) bool
		for _, part := range regexp.MustCompile(`(?i)\s+AND\s+|\s*&&\s*`).Split(orPart, -1) {
			part = strings.Trim(strings.TrimSpace(part), "()")
			m := predicateCond.FindStringSubmatch(part)
			if m == nil {
				return nil, fmt.Errorf("%w: %s", ErrInvalidSelector, s)
			}
			attr, op, val := m[1], strings.ToUpper(m[2]), m[3]+m[4]+m[5]
			if attr == "elementType" || attr == "wdType" {
				attr = "type"
			}
			if attr == "wdName" {
				attr = "name"
			}
			if attr == "wdValue" {
				attr = "value"
			}
			if attr == "wdLabel" {
				attr = "label"
			}
			if attr == "wdVisible" || attr == "isWDVisible" {
				attr = "visible"
			}
			if attr == "wdEnabled" || attr == "isWDEnabled" {
				attr = "enabled"
			}
			lower := strings.HasSuffix(op, "[C]")
			op = strings.TrimSuffix(op, "[C]")
			switch strings.ToUpper(val) {
			case "YES", "TRUE", "1":
				if attr == "visible" || attr == "enabled" || attr == "accessible" {
					val = "true"
				}
			case "NO", "FALSE", "0":
				if attr == "visible" || attr == "enabled" || attr == "accessible" {
					val = "false"
				}
			}
			a, o, v := attr, op, val
			conds = append(conds, func(e *Element) bool {
				got, ok := IOSAttr(e, a)
				if !ok {
					return false
				}
				want := v
				if lower {
					got, want = strings.ToLower(got), strings.ToLower(want)
				}
				switch o {
				case "==", "=":
					return got == want
				case "!=":
					return got != want
				case "CONTAINS":
					return strings.Contains(got, want)
				case "BEGINSWITH":
					return strings.HasPrefix(got, want)
				case "ENDSWITH":
					return strings.HasSuffix(got, want)
				case "LIKE":
					re := "^" + strings.ReplaceAll(strings.ReplaceAll(regexp.QuoteMeta(want), `\*`, ".*"), `\?`, ".") + "$"
					ok, _ := regexp.MatchString(re, got)
					return ok
				}
				return false
			})
		}
		groups = append(groups, conds)
	}
	return func(e *Element) bool {
		for _, g := range groups {
			ok := true
			for _, c := range g {
				if !c(e) {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	}, nil
}

var classChainPart = regexp.MustCompile("^(XCUIElementType\\w+|\\*)(?:\\[`(.*)`\\])?(?:\\[(-?\\d+)\\])?$")

func findClassChain(all []*Element, chain string) ([]*Element, error) {
	parts := strings.Split(strings.TrimPrefix(chain, "**/"), "/")
	last := parts[len(parts)-1]
	m := classChainPart.FindStringSubmatch(last)
	if m == nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidSelector, chain)
	}
	var pred func(*Element) bool
	if m[2] != "" {
		p, err := parsePredicate(m[2])
		if err != nil {
			return nil, err
		}
		pred = p
	}
	var out []*Element
	for _, e := range all {
		if e.Kind == KindContainer {
			continue
		}
		if m[1] != "*" && IOSType(e) != m[1] {
			continue
		}
		if pred != nil && !pred(e) {
			continue
		}
		out = append(out, e)
	}
	if m[3] != "" {
		var idx int
		fmt.Sscan(m[3], &idx)
		if idx < 0 {
			idx = len(out) + idx + 1
		}
		if idx < 1 || idx > len(out) {
			return nil, nil
		}
		return []*Element{out[idx-1]}, nil
	}
	return out, nil
}
