# Zebrunner MCloud dummy devices

mcloud-dummy emulates Android and iOS devices for testing the [Zebrunner MCloud](https://github.com/zebrunner/mcloud) device farm without hardware. A dummy is not an emulator: it speaks the protocols the farm uses to reach a device and answers them as a phone does, so the real farm ([mcloud-agent](https://github.com/zebrunner/mcloud-agent), the connectors, [STF](https://github.com/zebrunner/stf), Appium, [mcloud-grid](https://github.com/zebrunner/mcloud-grid)) runs unchanged on top of it. One small process serves hundreds of devices (about 10 MB idle for 200 of them).

| Platform | What the dummy is | How the farm reaches it (mcloud-agent) |
| --- | --- | --- |
| Android | a fake adbd on a TCP port | `connection: android-sim`, `address: <host>:<port>`: the connector runs `adb connect` |
| iOS | a fake WebDriverAgent and its MJPEG stream | `connection: proxy`, `address: <host>`, `wda_port`, `mjpeg_port`: the connector relays them |

Every device shows a small fake app on its synthetic screen: a button with a counter, an input field, a reset button, the clock and the recent touches. A tap of the STF UI, a click of Appium or `adb shell input` lands on the same app, so a test sees the effect of the others: STF shows the counter an Appium test increments.

The image is a static Go program on Alpine, built for `linux/amd64` and `linux/arm64`.

## Image contents

| Component | Version |
| --- | --- |
| Base image | `alpine:3.24.3` (pinned by digest), no package added |
| `mcloud-dummy` | built by `golang:1.26.5-alpine` (pinned by digest) without cgo, `/opt/zebrunner/mcloud-dummy` |
| [mvdan/sh](https://github.com/mvdan/sh) | 3.14.1: the POSIX shell interpreter of the Android shell |
| [androidbinary](https://github.com/shogo82148/androidbinary) | 1.0.6: the manifests of the installed APKs |
| [xmlquery](https://github.com/antchfx/xmlquery) | 1.5.2: the xpath locators of Appium |
| [golang.org/x/image](https://pkg.go.dev/golang.org/x/image) | 0.48.0: the fonts of the synthetic screen |
| `/opt/zebrunner/devices.yml` | `examples/devices.yml`: 3 Android (2 phones, a tablet) and 2 iOS devices |

## Build

```bash
make build VERSION=0.1   # public.ecr.aws/zebrunner/mcloud-dummy:0.1 in the local docker
make push VERSION=0.1    # built by docker buildx and pushed: docker login public.ecr.aws first
make binary              # bin/mcloud-dummy for this machine
```

`make build` builds the image for the platform of this machine, `make push` builds `linux/amd64` and `linux/arm64` (`PLATFORMS`) with `docker buildx` and pushes them as one multi-platform image. `IMAGE_REPO` sets another repository, `VERSION` is the tag and the `org.opencontainers.image.version` label (`dev` by default, required by `push`). `make push` uses the buildx builder `mcloud` (docker-container driver), which it creates once.

The file `VERSION` is the release in work (bumped by hand after a release, CONVENTIONS.md#versions). The `publish` job of CI pushes the image of every merge into `develop` after all the checks passed: `public.ecr.aws/zebrunner/mcloud-dummy:<VERSION>-develop-<sha>` (7 characters of the commit) and `<VERSION>-develop-latest`, the newest commit of `develop`.

## Run

```bash
make run                                   # 1 Android (adb 127.0.0.1:15555) and 1 iOS (WDA 18100, MJPEG 18101)
adb connect 127.0.0.1:15555 && adb shell getprop ro.product.model
open http://127.0.0.1:8800/                # the dashboard: the screens, the faults
```

In the network of the devices of the farm (`STF_DOCKER_NETWORK` of mcloud-agent), with the devices of a file:

```bash
docker run -d --name mcloud-dummy --network infra --restart unless-stopped -p 8800:8800 \
  -e DUMMY_HOST=mcloud-dummy -v "$PWD/devices.yml:/opt/zebrunner/devices.yml:ro" \
  public.ecr.aws/zebrunner/mcloud-dummy:<version>
```

## Configuration

The variables of the container (the program takes them as the flags `-config`, `-host`, `-control` and `-log-level` too):

| Variable | Default | Description |
| --- | --- | --- |
| `DUMMY_CONFIG` | `/opt/zebrunner/devices.yml` | the YAML file of the devices, see below; without it (`make run`) the flags `-android <n>` and `-ios <n>` start numbered default devices |
| `DUMMY_HOST` | the hostname | the address the farm reaches the dummies at (e.g. the name of the container): shown by the control API and reported by WebDriverAgent |
| `DUMMY_CONTROL` | `:8800` | the listen address of the control API and the dashboard |
| `LOGGER_LEVEL` | `INFO` | the minimum level of the log: `DEBUG` (also every shell command of a device), `INFO`, `WARN`, `ERROR` |

The devices file (`examples/devices.yml` is a complete example): an entry with `count` greater than 1 is expanded into numbered devices with consecutive ports (iOS: two ports per device, WebDriverAgent and MJPEG). An unknown field is an error.

| Field | Default | Description |
| --- | --- | --- |
| `control`, `host` | | the defaults of `DUMMY_CONTROL` and `DUMMY_HOST` |
| `platform` | | `android` or `ios` |
| `count` | `1` | the number of devices of the entry |
| `serial` | `dummy-android-`, `00008110-000A1B2C3D4E0000` | the serial (Android `ro.serialno`) or the UDID (iOS); a prefix numbered with `count` |
| `name`, `model`, `manufacturer`, `brand`, `device` | | the identity of the device (`ro.product.*`, the device info of WebDriverAgent) |
| `type` | `phone` | `phone`, `tablet` or `tv`: `ro.build.characteristics`, the interface idiom of iOS |
| `osVersion`, `sdk`, `abi` | `14`, by the version, `x86_64` | the Android version, its API level and the ABI (STF installs minicap and minitouch of the ABI) |
| `width`, `height` | `720`x`1280` pixels, `390`x`844` points | the screen of the natural orientation |
| `density`, `scale` | `320`, `3` | the density of Android (dpi), the scale of iOS |
| `port` | `15555` | Android: the adb port |
| `wdaPort`, `mjpegPort` | `18100`, `wdaPort` + 1 | iOS: the ports of WebDriverAgent and of its MJPEG stream |
| `auth` | `none` | Android: the adb authentication, `none`, `accept` or `deny` (the device stays `unauthorized`) |
| `bootTime`, `rebootTime` | `3s` (iOS `1s`), `10s` | how long the device boots (`sys.boot_completed`, `dumpsys display`) and how long it is gone during a reboot |
| `fps` | `10` | the maximum frame rate of the screen streams |
| `mjpegScale` | `100` | iOS: the size of the MJPEG frames in percent of the screen pixels, lower for many devices |
| `battery`, `charging` | `100`, `true` | the battery level in percent and whether it is plugged (`dumpsys battery`, the battery events of STFService, the battery info of UiAutomator2 and WebDriverAgent, the status bar) |
| `batteryRate` | `0` | percent per minute the battery discharges unplugged and charges plugged; `0` keeps the level |
| `network` | `wifi` | `wifi`, `cellular` or `none` (`dumpsys connectivity`, `gsm.network.type`, the connectivity events of STFService, the networks of UiAutomator2) |
| `rotation` | `0` | the orientation at the start: `0`, `90`, `180` or `270` |
| `locale`, `timeZone` | `en-US`, `UTC` | `persist.sys.locale`, `persist.sys.timezone`, the device info of UiAutomator2 and WebDriverAgent, the clock of the screen |
| `profile` | | the name of a profile the entry starts from, see [Fleets](#fleets) |
| `vary`, `pick`, `seed` | `pick: random`, `seed: 0` | a value per device of the entry, see [Fleets](#fleets) |

### Fleets

`examples/fleet.yml` is a complete example. `profiles` are named partial devices, e.g. the models of a farm, with the fields of a device; an entry takes one with `profile`, its own fields override the profile. `vary` gives every device of an entry its own value of a field: a list (`[wifi, cellular]`, a repeated value is picked more often), a range of numbers (`"5-100"`), `profile` picks the profiles and `screen` sets the size with the density (Android) or the scale (iOS) at once (`1080x2400@420`). `pick: random` picks by `seed`: the same configuration makes the same devices; `pick: cycle` takes the values in turn and spreads a range evenly. The serial, the ports and the numbering are not varied.

```yaml
devices:
  - platform: android
    count: 50
    seed: 1
    vary:
      profile: [pixel-8-pro, pixel-8-pro, galaxy-s24]
      battery: "5-100"
      charging: [true, false]
      network: [wifi, wifi, cellular, none]
      rotation: [0, 90]
      locale: [en-US, de-DE]
```

`mcloud-dummy clone` makes such a configuration of the devices of a real farm: a profile per model and OS version (the screen in pixels and dpi of Android, in points and scale of iOS) and an entry per platform that picks the profiles as often as the farm has them, with a random battery, charging and network. It reads `GET /api/v1/devices` of STF with an access token (`STF_TOKEN`):

```bash
STF_TOKEN=... mcloud-dummy clone -stf https://farm.example.com -count 30 -o fleet.yml
```

`mcloud-dummy farm-devices` prints the `devices:` list of mcloud-agent for a configuration (Android `android-sim` with `address <host>:<port>`, iOS `proxy` with the WebDriverAgent and MJPEG ports, the names without spaces, a block of `-port-step` ports per device from `-port-start` for Appium and STF), or replaces the `devices:` section of the vars of the agent in place with `-vars`:

```bash
mcloud-dummy farm-devices -config fleet.yml -host mcloud-dummy -port-start 8600 -vars mcloud-agent/roles/devices/vars/main.yml
```

In the image: `docker run --rm -v $PWD:/w mcloud-dummy farm-devices -config /w/fleet.yml -host mcloud-dummy`.

## What is emulated

### Android

- The adb transport: CNXN with the features `shell_v2,cmd,stat_v2,ls_v2,fixed_push_mkdir`, the authentication (`none`, `accept`, `deny`), the flow control of the streams.
- The services `shell:` (v1, v2 with the exit code, stdin, pty), `exec:`, `sync:` (STAT, STA2, LST2, LIST, LIS2, SEND, RECV), the local sockets of `adb forward` (`localabstract:`, `tcp:`), `reboot:`, `root:`, `tcpip:`, `reverse:list-forward`, `track-jdwp`.
- A shell: the commands go through a POSIX shell interpreter (pipes, `&&`, `export`, `$(...)`, the redirections into an in-memory file system), the commands of the device are implemented by the program: `getprop`, `setprop`, `settings`, `pm`, `cmd package` (the streamed install), `am` (start, services, broadcast, instrument, force-stop), `dumpsys` (display, window, package, activity, power, battery with `set level`, `set ac`, `unplug` and `reset`, connectivity, wifi, input_method...), `svc wifi|data enable|disable`, `wm`, `input`, `screencap`, `ime`, `logcat` (text and the binary `-B` of adbkit), `ps`, `pidof`, `pgrep`, `kill`, the toybox basics (`ls`, `cat`, `rm`, `chmod`, `grep`, `df`...). An unknown command answers `inaccessible or not found` (127) and is logged.
- A package manager that reads the manifest of an installed APK (the package, the version, the instrumentation), so what Appium and STF install is installed with its version.
- The processes: what STF and Appium start keeps running until it is killed or its shell ends.
- STF: minicap (the binary and the apk mode: `-i`, `-s`, `-S` with the banner and the JPEG frames of the projection; the virtual size STF asks for, the box of the screen in the browser, is fitted to the aspect ratio of the display as minicap does, the frames are turned upright by the rotation, `QUIRK_ALWAYS_UPRIGHT`), minitouch (the banner, `d`, `m`, `u`, `c`, `w`, `r`), STFService (the sockets `stfagent` and `stfservice`, the protobuf envelopes of the display, the properties, the battery, the clipboard, the browsers, the rotation, the keys, the typing and the events), minirev.
- Appium UiAutomator2: `am instrument` of `io.appium.uiautomator2.server.test` starts a fake UiAutomator2 server on `tcp:6790` of the device with the routes of the real server 9.10.5 and none other (`tests/contract/uia2-routes-9.10.5.txt`): one session, find by id, accessibility id, class name, xpath and UiSelector (`strategy`, `selector` and `context` of the driver), click, value, text, the 44 attributes of the real server, rect, source, screenshots, W3C actions, keys, settings, device info, windows and displays, orientation. Its responses and errors follow the recordings of real devices, see [Contract tests](#contract-tests); the server quits after its session, as the real one does.

### iOS

- `/status` (the OS, the build and the current session STF reads), `/health`, the sessions (several at once, or a single one as the real WebDriverAgent with the fault `singleSession`).
- `/wda/device/info` (the idiom of a phone, a tablet or a TV), `/wda/screen`, `/window/size`, `/wda/batteryInfo`, the orientation and the rotation, lock and unlock, home and the buttons, the apps (launch, activate, terminate, state, the active app), the pasteboard, the settings.
- Find by accessibility id, class name, xpath, `-ios predicate string` and `-ios class chain`; click, value, clear, text, attributes, rect, source (xml, json, description), screenshots, W3C actions, `wda/tap`, drag, keys.
- The MJPEG server of WebDriverAgent: a raw TCP server that starts the HTTP/1.0 `multipart/x-mixed-replace` response at once (STF reads the plain socket and sends no request), with the settings `mjpegServerFramerate`, `mjpegScalingFactor`, `mjpegServerScreenshotQuality` and `mjpegFixOrientation`: the frames are of the natural orientation (portrait) as the framebuffer of a real device, a landscape screen turned unless `mjpegFixOrientation` is set.

The connector, STF and Appium use the `proxy` connection (`WIRELESS=true`): what needs usbmuxd or go-ios (the battery of STF, the install of an app by STF, `ios reboot`) is unavailable as for any proxied device.

## In the farm

The dummies are ordinary devices of `roles/devices/vars/main.yml` of mcloud-agent, e.g. with the container `mcloud-dummy` in the network of the devices:

```yaml
devices:
  - id: dummy-android-001
    os: android
    name: Dummy_Pixel_1
    connection: android-sim
    address: mcloud-dummy:15555
    appium_port: 8601
    min_port: 8603
    max_port: 8610
  - id: 00008110-000A1B2C3D4E0001
    os: ios
    name: Dummy_iPhone_1
    connection: proxy
    address: mcloud-dummy
    wda_port: 18100
    mjpeg_port: 18101
    appium_port: 8661
    min_port: 8663
    max_port: 8670
```

The iOS devices need `WDA_FILE: /dev/null` in the settings of mcloud-agent while its template turns an empty `WDA_FILE` into the volume `'/dev/null'` (with the quotes), which `docker run` refuses.

[lab/README.md](lab/README.md) describes the stand mcloud-dummy was verified on: an MCloud server and a node of dummy devices on one host, built from the develop branches.

## Control API

| Request | Description |
| --- | --- |
| `GET /` | the dashboard: the live screens (a click taps), the faults, the reboot |
| `GET /healthz` | `ok`: the healthcheck of the image |
| `GET /api/devices` | the state of every device: booted, the adb connections, the processes and sockets, the installed packages, the opened services, the requests of WebDriverAgent, the counter and the input of the app |
| `GET /api/devices/{serial}` | the state and the recent events: taps, typing, sessions, installs, unknown commands |
| `GET /api/devices/{serial}/commands` | Android: the recent shell commands with their exit codes and durations; iOS: the WebDriverAgent requests by route |
| `GET /api/devices/{serial}/screen` | the screen as a JPEG, `?width=360` scales it, `&format=png` |
| `POST /api/devices/{serial}/faults` | the failures below, the omitted ones are kept |
| `POST /api/devices/{serial}/reboot` | reboots the device: Android closes the adb port for `rebootTime` and kills the processes, iOS closes its ports and drops the sessions |
| `POST /api/devices/{serial}/disconnect` | Android: drops the adb connections, the device is offline until the connector reconnects; iOS: drops the WebDriverAgent sessions |
| `POST /api/devices/{serial}/crash?process=minicap` | Android: kills the processes of the name (`minicap`, `minitouch`, `stf.agent`, `jp.co.cyberagent.stf`, `io.appium.uiautomator2.server`...) |
| `POST /api/devices/{serial}/input` | `{"tap":[x,y]}`, `{"swipe":[x1,y1,x2,y2]}`, `{"text":"..."}`, `{"key":"HOME"}`, `{"rotate":90}`, `{"lock":true}` |
| `POST /api/devices/{serial}/state` | `{"battery":5,"charging":false,"network":"none","rotation":90}`, the omitted fields are kept; STF and Appium see the change |
| `POST /api/state` | the same for every device, `?platform=android` or `ios` for the devices of a platform |

The faults (`latency` is a duration such as `"500ms"` or a number of milliseconds):

| Platform | Fault | Effect |
| --- | --- | --- |
| Android | `offline` | the adb port is closed: the device is unplugged |
| Android | `auth` | `deny`: the host is never authorized, the connector reports `unauthorized`; `none`, `accept` |
| Android | `latency` | a delay before every opened service |
| Android | `bootStuck` | `sys.boot_completed` stays empty, `dumpsys display` has no display |
| Android | `noScreen` | `localabstract:minicap` refuses the connections |
| iOS | `down` | the port of WebDriverAgent is closed |
| iOS | `notReady` | WebDriverAgent answers an error |
| iOS | `noMjpeg` | the MJPEG port is closed, the streams end |
| iOS | `latency` | a delay before every response of WebDriverAgent |
| iOS | `singleSession` | a new session invalidates the previous one, as the real WebDriverAgent does |

```bash
curl -X POST localhost:8800/api/devices/dummy-android-001/faults -d '{"auth":"deny"}'
curl -X POST localhost:8800/api/devices/dummy-android-001/faults -d '{"auth":"none","latency":"300ms"}'
curl -X POST 'localhost:8800/api/state?platform=ios' -d '{"battery":4,"charging":false}'
```

## Contract tests

`tests/contract/uia2_contract.py` keeps the fake UiAutomator2 server equal to the real one. A scenario of 64 requests that needs nothing of the screen but the `FrameLayout` of a window (the session, the settings, the device info, the finds by every strategy and their errors, the attributes, the screenshots, the windows, the unknown routes and sessions) is recorded against a real server and replayed against the fake:

| Command | What it does |
| --- | --- |
| `record <url> <fixture> <source>` | runs the scenario against a real server (the port Appium forwards to a device of a farm, e.g. `http://<connector>:8200/wd/hub`) and writes the status, the W3C error and the shape of every response (the keys and their JSON types) with a short sample, the identifiers of the device redacted |
| `verify <url> <fixture>...` | runs the scenario against the fake and compares it with every fixture: the status and the error must be the ones of the real server, every key it has must be there with its type (a type that differs between the devices is not checked) |
| `routes <url> <routes.txt>` | probes every route of the real server (parsed from `AppiumServlet.java` of its version) on the fake: none may be `unknown command`; never against a real device, the probes press back and open the notifications |

`tests/contract/fixtures/` has the recordings of a Pixel 8 Pro (Android 16) and a Galaxy S24 (Android 14) with the UiAutomator2 server 9.10.5 of the Appium image of the farm; `make contract UIA2=<url>` verifies a dummy against them. The Go test `TestUIA2RoutesAreTheRealOnes` checks that the fake registers exactly the routes of `tests/contract/uia2-routes-9.10.5.txt`. A new version of the server: record its fixtures on a device of a farm, extract its routes, set `uia2Version` of `internal/android/uia2.go`.

## Limitations

- No USB: the Android dummies are network devices (`android-sim`), the iOS ones a proxied WebDriverAgent (`proxy`).
- The apps do not run: an installed APK is a package with a version, `am start` makes its process run, the screen always shows the fake app.
- Not implemented: the `framebuffer:` service, the reverse forwarding (`adb reverse`; minirev accepts and drops), `abb`, the `sendrecv_v2` transfers (not advertised, the clients fall back), the iOS features of usbmuxd and go-ios.
- The WebDriverAgent server implements the commands of the usual tests, another command answers `unknown command` and is logged in the events of the device; it has no contract tests yet.

## Exit codes and health

- Exit code `0` means a problem a restart does not fix: an invalid configuration (the devices file, `LOGGER_LEVEL`), logged before the exit.
- Exit code `1` asks for a restart: a port of a device or of the control API can't be opened (still in use).
- The image healthcheck runs `mcloud-dummy -healthcheck`: `GET /healthz` of the control API.

## Development

`make help` lists the targets, `make check` runs the checks as CI does (every step runs, the failed ones fail the target):

| Target | What it checks |
| --- | --- |
| `make lint` | `tests/lint.sh`: yamllint, actionlint, hadolint, pymarkdown, `bash -n`, ShellCheck and shfmt, the text file checks of CONVENTIONS.md; the project checks of `tests/lint_project.sh`: gofmt, `go vet`, `go mod tidy -diff`, ruff of the Python tests. The tools are installed into `.venv` from `tests/requirements-lint.txt`; hadolint comes from `brew install hadolint` on macOS. |
| `make test` | the Go tests with the race detector, no docker and no device needed: the adb protocol against a minimal adb host (handshake, auth, shell v1 and v2, stdin, the flow control of large data, sync push, stat and pull), the shell commands STF, Appium and the connector run, WebDriverAgent and MJPEG, the locators of the app, the configuration. With `JUNIT_DIR` gotestsum writes their JUnit XML. |
| `make docker` | `tests/image_test.sh`: builds the image (`IMAGE` overrides its tag, `REBUILD=true` rebuilds it), checks its configuration, the exit codes of an invalid configuration and the startup of the example devices. |
| `make e2e HUB=<url>` | `tests/e2e.py`: an Appium session per platform through a grid (create, screenshot, source, find by id and xpath, click, send keys, W3C actions, delete) |
| `make contract UIA2=<url>` | `tests/contract/uia2_contract.py verify`: the fake UiAutomator2 server of a dummy against the recordings of the real one, see [Contract tests](#contract-tests) |

`tests/screen.py <host> <screen port> [seconds] [out.jpg] [size]` reads the screen websocket of the STF provider of a device, as the STF UI does: it asks for the size of a box of the browser (`360x640` by default) and prints the size of the frames, which keep the aspect ratio of the device.

The GitHub Actions workflow `.github/workflows/ci.yml` runs the same `lint`, `tests` and `docker` jobs on pushes and pull requests to `develop`, `main` and `master`. Dependabot (`.github/dependabot.yml`) opens weekly pull requests for the GitHub Actions, the base images of the `Dockerfile`, the Go modules and ruff.

The conventions of the style, the formatting and the tooling of this repository are in [CONVENTIONS.md](CONVENTIONS.md), the same in all the MCloud repositories.

## Support

Feel free to support the development with a [**donation**](https://www.paypal.com/donate/?hosted_button_id=MNHYYCYHAKUVA) for the next improvements.

<p align="center">
  <a href="https://zebrunner.com/"><img alt="Zebrunner" src="https://github.com/zebrunner/zebrunner/raw/master/docs/img/zebrunner_intro.png"></a>
</p>

## License

[Apache License 2.0](LICENSE)
