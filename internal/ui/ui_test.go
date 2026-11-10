package ui

import (
	"strings"
	"testing"
)

func testApp() *App {
	return New(Info{Platform: "android", Name: "Test", Model: "Pixel", OSVersion: "14", Serial: "s1", Width: 720, Height: 1280})
}

func TestTapActivatesElements(t *testing.T) {
	a := testApp()
	b := a.Find("tap_button")
	if hit := a.Tap("test", b.Rect.X+1, b.Rect.Y+1); hit != "tap_button" || a.Taps() != 1 {
		t.Fatalf("hit %q taps %d", hit, a.Taps())
	}
	if a.Find("counter").Text != "Taps: 1" {
		t.Fatalf("counter: %q", a.Find("counter").Text)
	}
	a.Click("test", "input")
	a.Type("test", "abc")
	a.Key("test", "DEL")
	if a.Input() != "ab" {
		t.Fatalf("input %q", a.Input())
	}
	a.Click("test", "reset_button")
	if a.Taps() != 0 || a.Input() != "" {
		t.Fatal("reset")
	}
}

func TestRotationSwapsTheSize(t *testing.T) {
	a := testApp()
	a.SetRotation("test", 90)
	if w, h := a.Size(); w != 1280 || h != 720 {
		t.Fatalf("%dx%d", w, h)
	}
}

func TestFindAndroid(t *testing.T) {
	a := testApp()
	root := a.Layout()
	for _, c := range []struct{ using, value, want string }{
		{"id", "com.zebrunner.dummy:id/tap_button", "tap_button"},
		{"id", "tap_button", "tap_button"},
		{"accessibility id", "input", "input"},
		{"class name", "android.widget.EditText", "input"},
		{"xpath", "//android.widget.Button[@text='Reset']", "reset_button"},
		{"xpath", "//*[@resource-id='com.zebrunner.dummy:id/counter']", "counter"},
		{"-android uiautomator", `new UiSelector().text("Tap me")`, "tap_button"},
		{"-android uiautomator", `new UiSelector().resourceId("com.zebrunner.dummy:id/input").clickable(true)`, "input"},
	} {
		found, err := FindAndroid(root, 0, c.using, c.value)
		if err != nil || len(found) == 0 || found[0].ID != c.want {
			t.Errorf("%s=%s: %v %v", c.using, c.value, found, err)
		}
	}
	if found, _ := FindAndroid(root, 0, "id", "nope"); len(found) != 0 {
		t.Error("nope found")
	}
	if _, err := FindAndroid(root, 0, "xpath", "//*["); err == nil {
		t.Error("an invalid xpath is an error")
	}
}

func TestFindIOS(t *testing.T) {
	a := New(Info{Platform: "ios", Model: "iPhone", OSVersion: "17.4", Width: 390, Height: 844})
	root := a.Layout()
	for _, c := range []struct{ using, value, want string }{
		{"accessibility id", "tap_button", "tap_button"},
		{"class name", "XCUIElementTypeTextField", "input"},
		{"xpath", "//XCUIElementTypeButton[@label='Reset']", "reset_button"},
		{"-ios predicate string", `type == "XCUIElementTypeButton" AND label BEGINSWITH "Tap"`, "tap_button"},
		{"-ios predicate string", `name == 'counter'`, "counter"},
		{"-ios class chain", "**/XCUIElementTypeButton[`name == \"reset_button\"`]", "reset_button"},
		{"-ios class chain", "**/XCUIElementTypeButton[2]", "reset_button"},
	} {
		found, err := FindIOS(root, "Dummy", c.using, c.value)
		if err != nil || len(found) == 0 || found[0].ID != c.want {
			t.Errorf("%s=%s: %v %v", c.using, c.value, found, err)
		}
	}
}

func TestSources(t *testing.T) {
	a := testApp()
	x := AndroidXML(a.Layout(), 0)
	if !strings.Contains(x, `resource-id="com.zebrunner.dummy:id/tap_button"`) || !strings.Contains(x, `bounds="[`) {
		t.Fatal(x)
	}
	if ElementUUID("a") == ElementUUID("b") || ElementUUID("a") != ElementUUID("a") {
		t.Fatal("element ids must be stable and distinct")
	}
}
