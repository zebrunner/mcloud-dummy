#!/usr/bin/env python3
"""Contract tests of the fake UiAutomator2 server of mcloud-dummy against the real one.

record <server url> <fixture.json> <source>: runs the scenario against a real UiAutomator2 server (a device of a farm,
    its port forwarded by Appium) and writes the status codes and the shapes (keys and JSON types) of the responses;
verify <server url> <fixture.json>...: runs the same scenario against another server (the dummy) and compares it
    with every recorded fixture: the status, the W3C error code, the keys and their types;
routes <server url> <routes.txt>: probes every route of the real server (AppiumServlet.java of its version) and
    reports the ones the server under test does not know (`unknown command`).

The scenario uses what every Android screen has (the FrameLayout of the window), so it records the same steps on any
device. A UiAutomator2 server allows one session: the scenario creates its own one and deletes it at the end, then the
real server quits, as after an Appium session.
"""

import json
import sys
import time
import urllib.error
import urllib.request

ELEMENT = "element-6066-11e4-a52e-4f735466cecf"
FRAME = "android.widget.FrameLayout"
MISSING_ELEMENT = "00000000-0000-0000-0000-00000000dead"
ATTRIBUTES = [
    "text",
    "resource-id",
    "class",
    "package",
    "content-desc",
    "bounds",
    "displayed",
    "enabled",
    "clickable",
    "focusable",
    "focused",
    "checkable",
    "checked",
    "selected",
    "scrollable",
    "long-clickable",
    "password",
    "index",
    "unknown-attribute",
]


def request(base, method, path, body=None, timeout=60):
    """the status and the decoded JSON body of a request; the body is the raw text when it is not JSON"""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            status, raw = resp.status, resp.read()
    except urllib.error.HTTPError as err:
        status, raw = err.code, err.read()
    try:
        return status, json.loads(raw or b"null")
    except ValueError:
        return status, raw.decode(errors="replace")


def shape(value):
    """the JSON type tree of a value: the keys of the objects, the shape of the first item of an array"""
    if isinstance(value, dict):
        return {k: shape(v) for k, v in sorted(value.items())}
    if isinstance(value, list):
        return [shape(value[0])] if value else []
    if isinstance(value, bool):
        return "bool"
    if isinstance(value, (int, float)):
        return "number"
    if value is None:
        return "null"
    return "string"


def element_id(body):
    value = body.get("value") if isinstance(body, dict) else None
    if isinstance(value, list) and value:
        value = value[0]
    if isinstance(value, dict):
        return value.get(ELEMENT) or value.get("ELEMENT")
    return None


def scenario():
    """the steps: (name, method, path, body, what the response gives to the next steps)"""
    s = "/session/{sid}"
    e = s + "/element/{el}"
    by_class = {"strategy": "class name", "selector": FRAME}
    missing = {"strategy": "id", "selector": "no.such:id/nothing"}
    pause = {"type": "pause", "duration": 10}
    actions = [{"type": "pointer", "id": "finger1", "parameters": {"pointerType": "touch"}, "actions": [pause]}]
    steps = [
        ("status", "GET", "/status", None, None),
        ("new session", "POST", "/session", {"capabilities": {"firstMatch": [{}], "alwaysMatch": {}}}, "sid"),
        ("session details", "GET", s, None, None),
        ("sessions", "GET", "/sessions", None, None),
        ("get settings", "GET", s + "/appium/settings", None, None),
        ("update settings", "POST", s + "/appium/settings", {"settings": {"waitForIdleTimeout": 0}}, None),
        ("device info", "GET", s + "/appium/device/info", None, None),
        ("system bars", "GET", s + "/appium/device/system_bars", None, None),
        ("pixel ratio", "GET", s + "/appium/device/pixel_ratio", None, None),
        ("display density", "GET", s + "/appium/device/display_density", None, None),
        ("battery info", "GET", s + "/appium/device/battery_info", None, None),
        ("window size", "GET", s + "/window/current/size", None, None),
        ("orientation", "GET", s + "/orientation", None, None),
        ("rotation", "GET", s + "/rotation", None, None),
        ("source", "GET", s + "/source", None, None),
        ("screenshot", "GET", s + "/screenshot", None, None),
        ("find by class name", "POST", s + "/element", by_class, "el"),
        ("find all by class name", "POST", s + "/elements", by_class, None),
        ("find by xpath", "POST", s + "/element", {"strategy": "xpath", "selector": f"//{FRAME}"}, None),
        (
            "find by uiautomator",
            "POST",
            s + "/element",
            {"strategy": "-android uiautomator", "selector": f'new UiSelector().className("{FRAME}")'},
            None,
        ),
        ("find with the W3C fields", "POST", s + "/element", {"using": "class name", "value": FRAME}, None),
        ("find a missing element", "POST", s + "/element", missing, None),
        ("find all missing elements", "POST", s + "/elements", missing, None),
        ("find by an invalid xpath", "POST", s + "/element", {"strategy": "xpath", "selector": "//*["}, None),
        ("find by an unknown strategy", "POST", s + "/element", {"strategy": "bogus", "selector": "x"}, None),
        ("find a child", "POST", e + "/element", {"strategy": "xpath", "selector": ".//*"}, None),
        ("find children", "POST", e + "/elements", {"strategy": "xpath", "selector": ".//*"}, None),
        ("element text", "GET", e + "/text", None, None),
        ("element name", "GET", e + "/name", None, None),
        ("element rect", "GET", e + "/rect", None, None),
        ("element size", "GET", e + "/size", None, None),
        ("element location", "GET", e + "/location", None, None),
        ("element screenshot", "GET", e + "/screenshot", None, None),
        ("active element", "GET", s + "/element/active", None, None),
    ]
    steps += [(f"attribute {a}", "GET", e + f"/attribute/{a}", None, None) for a in ATTRIBUTES]
    steps += [
        ("a missing element id", "GET", s + f"/element/{MISSING_ELEMENT}/text", None, None),
        ("alert text without an alert", "GET", s + "/alert/text", None, None),
        ("actions with a pause", "POST", s + "/actions", {"actions": actions}, None),
        ("clipboard", "POST", s + "/appium/device/get_clipboard", {"contentType": "plaintext"}, None),
        ("list windows", "POST", s + "/appium/list_windows", {}, None),
        ("list displays", "POST", s + "/appium/list_displays", {}, None),
        ("an unknown command", "GET", s + "/no/such/command", None, None),
        ("a route of the driver only (window rect)", "GET", s + "/window/rect", None, None),
        ("a route of the driver only (displayed)", "GET", e + "/displayed", None, None),
        ("an unknown session", "GET", "/session/no-such-session/orientation", None, None),
        ("delete session", "DELETE", s, None, None),
    ]
    return steps


def run(base):
    """runs the scenario; the results: name, method, path, status, body"""
    ctx = {"sid": "none", "el": MISSING_ELEMENT}
    results = []
    for name, method, path, body, gives in scenario():
        start = time.time()
        try:
            status, resp = request(base, method, path.format(**ctx), body)
        except OSError as err:
            status, resp = 0, f"connection failed: {err}"
        took = time.time() - start
        if gives == "sid" and isinstance(resp, dict):
            value = resp.get("value") if isinstance(resp.get("value"), dict) else {}
            ctx["sid"] = resp.get("sessionId") or value.get("sessionId") or "none"
        elif gives == "el" and element_id(resp):
            ctx["el"] = element_id(resp)
        results.append({"name": name, "method": method, "path": path, "status": status, "body": resp})
        print(f"  {status:3} {took:5.2f}s {name}", file=sys.stderr)
    return results


# the values of a device or of a farm the fixtures do not keep: the samples show their type only
REDACTED = {"androidId", "SSID", "extraInfo", "physicalDisplayId", "physicalId", "deviceUDID", "udid", "deviceName"}


def sample(body):
    """a short sample of a body for the fixture: long strings (screenshots, sources) are cut, the ids redacted"""
    if isinstance(body, dict):
        return {k: "<redacted>" if k in REDACTED and v is not None else sample(v) for k, v in body.items()}
    if isinstance(body, list):
        return [sample(v) for v in body[:2]]
    if isinstance(body, str) and len(body) > 160:
        return body[:160] + f"... ({len(body)} characters)"
    return body


def error_code(body):
    value = body.get("value") if isinstance(body, dict) else None
    return value.get("error") if isinstance(value, dict) else None


def record(base, path, source):
    steps = [
        {
            "name": r["name"],
            "method": r["method"],
            "path": r["path"],
            "status": r["status"],
            "error": error_code(r["body"]),
            "shape": shape(r["body"]),
            "sample": sample(r["body"]),
        }
        for r in run(base)
    ]
    fixture = {"server": "uiautomator2", "source": source, "recorded": time.strftime("%Y-%m-%d"), "steps": steps}
    with open(path, "w") as f:
        json.dump(fixture, f, indent=2)
        f.write("\n")
    return True


def compare(expected, actual, where):
    """the differences of two shapes: what the real server has and the tested one has not, the other types"""
    if isinstance(expected, dict):
        if not isinstance(actual, dict):
            return [f"{where}: an object is expected, got {json.dumps(actual)[:80]}"]
        diffs = []
        for key, sub in expected.items():
            diffs += [f"{where}.{key}: missing"] if key not in actual else compare(sub, actual[key], f"{where}.{key}")
        return diffs
    if isinstance(expected, list):
        if not isinstance(actual, list):
            return [f"{where}: an array is expected, got {json.dumps(actual)[:80]}"]
        return compare(expected[0], actual[0], f"{where}[0]") if expected and actual else []
    if expected != actual and "null" not in (expected, actual):
        return [f"{where}: {expected} is expected, got {json.dumps(actual)[:80]}"]
    return []


def verify(base, fixtures):
    recorded = []
    for path in fixtures:
        with open(path) as f:
            recorded.append(json.load(f))
    results = {r["name"]: r for r in run(base)}
    failures = 0
    steps = recorded[0]["steps"]
    for i, step in enumerate(steps):
        variants = [step] + [fx["steps"][i] for fx in recorded[1:]]
        got = results[step["name"]]
        problems = []
        statuses = sorted({v["status"] for v in variants})
        errors = {v["error"] for v in variants}
        if got["status"] not in statuses:
            problems.append(f"status {got['status']}, the real server answers {statuses}")
        if error_code(got["body"]) not in errors:
            problems.append(f"error {error_code(got['body'])!r}, the real server answers {sorted(map(str, errors))}")
        if not problems:
            # one recording matched is enough: the types that differ between the devices are not checked
            diffs = [compare(v["shape"], shape(got["body"]), "body") for v in variants]
            if all(diffs):
                problems += diffs[0]
        failures += bool(problems)
        print(f"{'ok  ' if not problems else 'FAIL'} {step['method']:6} {step['path']:45} {step['name']}")
        for p in problems:
            print(f"       {p}")
    sources = ", ".join(fx["source"] for fx in recorded)
    print(f"\n{len(steps) - failures} of {len(steps)} steps match the real server ({sources})")
    return failures == 0


def routes(base, path):
    """probes every route of the real server with a new session; a route is known when it is not `unknown command`"""
    _, resp = request(base, "POST", "/session", {"capabilities": {"firstMatch": [{}], "alwaysMatch": {}}})
    sid = resp.get("sessionId") or resp["value"]["sessionId"]
    with open(path) as f:
        table = [line.split() for line in f if line.strip()]
    unknown = 0
    for method, route, handler in table:
        if route in ("/session/:sessionId", "/session"):
            # the probe keeps its session: a new one or its end would make the next routes answer an invalid session
            continue
        url = route.replace(":sessionId", sid).replace(":id2", MISSING_ELEMENT).replace(":id", MISSING_ELEMENT)
        url = url.replace(":name", "text").replace(":windowHandle", "current")
        status, resp = request(base, method, url, {} if method == "POST" else None)
        known = error_code(resp) not in ("unknown command", "unknown method")
        unknown += not known
        print(f"{'ok  ' if known else 'FAIL'} {method:6} {route:60} {handler} -> {status} {error_code(resp) or ''}")
    request(base, "DELETE", f"/session/{sid}")
    print(f"\n{len(table) - unknown} of {len(table)} routes of the real server are known")
    return unknown == 0


def main():
    if len(sys.argv) < 4 or sys.argv[1] not in ("record", "verify", "routes"):
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    command, base = sys.argv[1], sys.argv[2].rstrip("/")
    if command == "record":
        ok = record(base, sys.argv[3], " ".join(sys.argv[4:]) or base)
    elif command == "verify":
        ok = verify(base, sys.argv[3:])
    else:
        ok = routes(base, sys.argv[3])
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
