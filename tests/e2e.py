#!/usr/bin/env python3
"""E2E check of the dummy devices through the MCloud grid: a W3C session per platform with the basic commands.

usage: e2e.py <hub url> [android|ios|all] [udid]
"""

import base64
import json
import ssl
import sys
import time
import urllib.error
import urllib.request

HUB = sys.argv[1].rstrip("/")
WHICH = sys.argv[2] if len(sys.argv) > 2 else "all"
UDID = sys.argv[3] if len(sys.argv) > 3 else ""
CTX = ssl.create_default_context()
CTX.check_hostname = False
CTX.verify_mode = ssl.CERT_NONE
ELEMENT = "element-6066-11e4-a52e-4f735466cecf"


def call(method, path, body=None, timeout=300):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(HUB + path, data=data, method=method, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, context=CTX, timeout=timeout) as r:
            return json.loads(r.read() or b"{}")
    except urllib.error.HTTPError as e:
        raise RuntimeError(f"{method} {path}: HTTP {e.code}: {e.read()[:600].decode(errors='replace')}") from None


def step(name, f):
    t = time.time()
    try:
        v = f()
    except Exception as e:  # noqa: BLE001
        print(f"  FAIL {name} ({time.time() - t:.1f}s): {e}")
        raise
    print(f"  ok   {name} ({time.time() - t:.1f}s)")
    return v


def element_id(ref):
    return ref.get(ELEMENT) or ref.get("ELEMENT")


def expect_no_such_element(f):
    try:
        f()
    except RuntimeError as e:
        if "no such element" in str(e):
            return
        raise
    raise AssertionError("the element was found")


def run(platform):
    caps = {"platformName": platform, "appium:automationName": "UiAutomator2" if platform == "android" else "XCUITest"}
    if UDID:
        caps["appium:udid"] = UDID
    print(f"== {platform}")
    resp = step(
        "create session", lambda: call("POST", "/session", {"capabilities": {"alwaysMatch": caps, "firstMatch": [{}]}})
    )
    sid = resp["value"]["sessionId"]
    got = resp["value"].get("capabilities", {})
    udid = got.get("appium:udid") or got.get("udid")
    name = got.get("appium:deviceName") or got.get("deviceName")
    print(f"       session {sid} on {udid} ({name})")
    s = f"/session/{sid}"
    using = "id" if platform == "android" else "accessibility id"
    try:
        png = step("screenshot", lambda: base64.b64decode(call("GET", s + "/screenshot")["value"]))
        assert png[:4] == b"\x89PNG" or png[:3] == b"\xff\xd8\xff", png[:8]
        src = step("page source", lambda: call("GET", s + "/source")["value"])
        assert "tap_button" in src, src[:200]
        win = step("window rect", lambda: call("GET", s + "/window/rect")["value"])
        print(f"       window {win}")
        bid = element_id(
            step(
                "find tap_button",
                lambda: call("POST", s + "/element", {"using": using, "value": "tap_button"})["value"],
            )
        )
        step("click", lambda: call("POST", f"{s}/element/{bid}/click", {}))
        step("click again", lambda: call("POST", f"{s}/element/{bid}/click", {}))
        xp = (
            "//*[@resource-id='com.zebrunner.dummy:id/counter']"
            if platform == "android"
            else "//XCUIElementTypeStaticText[@name='counter']"
        )
        cid = element_id(
            step(
                "find counter by xpath", lambda: call("POST", s + "/element", {"using": "xpath", "value": xp})["value"]
            )
        )
        text = step("counter text", lambda: call("GET", f"{s}/element/{cid}/text")["value"])
        print(f"       counter: {text}")
        assert text.startswith("Taps: "), text
        iid = element_id(
            step("find input", lambda: call("POST", s + "/element", {"using": using, "value": "input"})["value"])
        )
        step("send keys", lambda: call("POST", f"{s}/element/{iid}/value", {"text": "hello", "value": list("hello")}))
        val = step("input text", lambda: call("GET", f"{s}/element/{iid}/text")["value"])
        assert "hello" in val, val
        step(
            "missing element is 'no such element'",
            lambda: expect_no_such_element(lambda: call("POST", s + "/element", {"using": using, "value": "nope"})),
        )
        step(
            "tap by W3C actions",
            lambda: call(
                "POST",
                s + "/actions",
                {
                    "actions": [
                        {
                            "type": "pointer",
                            "id": "finger1",
                            "parameters": {"pointerType": "touch"},
                            "actions": [
                                {"type": "pointerMove", "duration": 0, "x": 50, "y": 50},
                                {"type": "pointerDown", "button": 0},
                                {"type": "pause", "duration": 50},
                                {"type": "pointerUp", "button": 0},
                            ],
                        }
                    ]
                },
            ),
        )
    finally:
        step("delete session", lambda: call("DELETE", s))


def main():
    ok = True
    for p in ["android", "ios"] if WHICH == "all" else [WHICH]:
        try:
            run(p)
        except Exception:  # noqa: BLE001
            ok = False
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
