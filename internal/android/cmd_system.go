package android

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zebrunner/mcloud-dummy/internal/adb"
	"github.com/zebrunner/mcloud-dummy/internal/ui"
)

func cmdGetprop(c *Cmd) int {
	if len(c.Args) > 1 {
		v := c.d.Prop(c.Args[1])
		if v == "" && len(c.Args) > 2 {
			v = c.Args[2]
		}
		c.printf("%s\n", v)
		return 0
	}
	props := c.d.Props()
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.printf("[%s]: [%s]\n", k, props[k])
	}
	return 0
}

func cmdSetprop(c *Cmd) int {
	if len(c.Args) < 3 {
		c.errorf("usage: setprop NAME VALUE\n")
		return 1
	}
	c.d.SetProp(c.Args[1], c.Args[2])
	return 0
}

func cmdSettings(c *Cmd) int {
	var args []string
	for i := 1; i < len(c.Args); i++ {
		if c.Args[i] == "--user" {
			i++
			continue
		}
		args = append(args, c.Args[i])
	}
	if len(args) < 2 {
		c.errorf("usage: settings [--user <USER_ID>] get|put|delete|list NAMESPACE [KEY [VALUE]]\n")
		return 255
	}
	verb, ns := args[0], args[1]
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	m, ok := c.d.settings[ns]
	if !ok {
		c.errorf("Invalid namespace '%s'\n", ns)
		return 255
	}
	switch verb {
	case "get":
		if len(args) < 3 {
			return 255
		}
		if v, ok := m[args[2]]; ok {
			c.printf("%s\n", v)
		} else {
			c.printf("null\n")
		}
	case "put":
		if len(args) < 4 {
			return 255
		}
		m[args[2]] = args[3]
	case "delete":
		if len(args) < 3 {
			return 255
		}
		delete(m, args[2])
		c.printf("Deleted 1 rows\n")
	case "list":
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			c.printf("%s=%s\n", k, m[k])
		}
	default:
		return 255
	}
	return 0
}

func (d *Device) listPackages(c *Cmd, args []string) int {
	flags, rest := splitFlags(args)
	filter := ""
	for _, r := range rest {
		if r != "0" {
			filter = r
		}
	}
	for _, p := range d.pkgs.List() {
		if flags["3"] && p.System || flags["s"] && !p.System {
			continue
		}
		if filter != "" && !strings.Contains(p.Name, filter) {
			continue
		}
		line := "package:"
		if flags["f"] {
			line += p.Path + "="
		}
		line += p.Name
		if flags["--show-versioncode"] {
			line += fmt.Sprintf(" versionCode:%d", p.VersionCode)
		}
		if flags["U"] {
			line += " uid:10123"
		}
		c.printf("%s\n", line)
	}
	return 0
}

func (d *Device) install(c *Cmd, data []byte, source string) int {
	pkg, err := d.pkgs.Install(data)
	if err != nil {
		c.printf("Failure [%v]\n", err)
		d.App.Log("pm", "install_failed", "%s: %v", source, err)
		return 1
	}
	d.procs.KillByName(pkg.Name, 9)
	d.App.Log("pm", "install", "%s %s (%d)", pkg.Name, pkg.VersionName, pkg.VersionCode)
	c.printf("Success\n")
	return 0
}

func (d *Device) uninstall(c *Cmd, args []string) int {
	_, rest := splitFlags(args)
	var pkg string
	for _, r := range rest {
		if r != "0" {
			pkg = r
		}
	}
	d.procs.KillByName(pkg, 9)
	if !d.pkgs.Uninstall(pkg) {
		c.printf("Failure [DELETE_FAILED_INTERNAL_ERROR]\n")
		return 1
	}
	d.App.Log("pm", "uninstall", "%s", pkg)
	c.printf("Success\n")
	return 0
}

func cmdPm(c *Cmd) int {
	d := c.d
	if len(c.Args) < 2 {
		c.errorf("usage: pm [list|path|install|uninstall|clear|grant|revoke|dump] ...\n")
		return 1
	}
	args := c.Args[2:]
	switch c.Args[1] {
	case "path":
		_, rest := splitFlags(args)
		for _, r := range rest {
			if p := d.pkgs.Get(r); p != nil {
				c.printf("package:%s\n", p.Path)
				return 0
			}
		}
		return 1
	case "list":
		if len(args) == 0 {
			return 1
		}
		switch args[0] {
		case "packages":
			return d.listPackages(c, args[1:])
		case "instrumentation":
			_, rest := splitFlags(args[1:])
			for _, line := range d.pkgs.Instrumentations() {
				if len(rest) == 0 || strings.Contains(line, "(target="+rest[0]+")") {
					c.printf("%s\n", line)
				}
			}
			return 0
		case "features":
			for _, f := range []string{"android.hardware.touchscreen", "android.hardware.touchscreen.multitouch",
				"android.hardware.wifi", "android.hardware.screen.portrait", "android.hardware.screen.landscape",
				"android.software.webview", "android.hardware.bluetooth", "android.hardware.camera"} {
				c.printf("feature:%s\n", f)
			}
			return 0
		case "users":
			c.printf("Users:\n\tUserInfo{0:Owner:c13} running\n")
			return 0
		case "permissions", "permission-groups", "libraries":
			return 0
		}
		return 1
	case "install", "install-existing":
		_, rest := splitFlags(args)
		if len(rest) == 0 {
			c.errorf("Error: no package specified\n")
			return 1
		}
		file := rest[len(rest)-1]
		data, err := d.fs.ReadFile(c.abs(file))
		if err != nil {
			c.printf("Failure [INSTALL_FAILED_INVALID_URI]\n")
			return 1
		}
		return d.install(c, data, file)
	case "uninstall":
		return d.uninstall(c, args)
	case "clear":
		_, rest := splitFlags(args)
		if len(rest) == 0 || d.pkgs.Get(rest[len(rest)-1]) == nil {
			c.printf("Failed\n")
			return 1
		}
		d.procs.KillByName(rest[len(rest)-1], 9)
		c.printf("Success\n")
		return 0
	case "grant", "revoke":
		_, rest := splitFlags(args)
		if len(rest) < 2 {
			return 1
		}
		pkg, perm := rest[len(rest)-2], rest[len(rest)-1]
		if !d.pkgs.Grant(pkg, perm, c.Args[1] == "grant") {
			c.errorf("Exception occurred while executing 'grant':\njava.lang.IllegalArgumentException: Unknown package: %s\n", pkg)
			return 255
		}
		return 0
	case "dump":
		if len(args) > 0 {
			if p := d.pkgs.Get(args[len(args)-1]); p != nil {
				c.printf("%s", p.Dumpsys())
			}
		}
		return 0
	case "enable", "disable", "disable-user", "default-state":
		if len(args) > 0 {
			state := map[string]string{"enable": "enabled", "disable": "disabled", "disable-user": "disabled-user", "default-state": "default"}[c.Args[1]]
			c.printf("Package %s new state: %s\n", args[len(args)-1], state)
		}
		return 0
	case "get-install-location":
		c.printf("0[auto]\n")
		return 0
	case "has-feature":
		c.printf("true\n")
		return 0
	case "resolve-activity":
		return cmdResolveActivity(c, args)
	}
	return 0
}

func cmdResolveActivity(c *Cmd, args []string) int {
	_, rest := splitFlags(args)
	if len(rest) == 0 {
		return 1
	}
	pkg := c.d.pkgs.Get(rest[len(rest)-1])
	if pkg == nil || pkg.Launcher == "" {
		c.printf("No activity found\n")
		return 0
	}
	act := pkg.Launcher
	if strings.HasPrefix(act, ".") {
		act = pkg.Name + act
	}
	c.printf("priority=0 preferredOrder=0 match=0x108000 specificIndex=-1 isDefault=true\n%s/%s\n", pkg.Name, act)
	return 0
}

func cmdCmd(c *Cmd) int {
	d := c.d
	if len(c.Args) < 2 {
		return 0
	}
	args := c.Args[2:]
	switch c.Args[1] {
	case "package":
		if len(args) == 0 {
			return 0
		}
		switch args[0] {
		case "install":
			size := -1
			var file string
			for i := 1; i < len(args); i++ {
				if args[i] == "-S" && i+1 < len(args) {
					i++
					size, _ = strconv.Atoi(args[i])
				} else if !strings.HasPrefix(args[i], "-") && strings.Contains(args[i], "/") {
					file = args[i]
				}
			}
			var data []byte
			if size >= 0 {
				data = make([]byte, size)
				if _, err := io.ReadFull(c.Stdin, data); err != nil {
					c.printf("Failure [INSTALL_FAILED_INVALID_APK: short read]\n")
					return 1
				}
			} else if file != "" {
				var err error
				if data, err = d.fs.ReadFile(c.abs(file)); err != nil {
					c.printf("Failure [INSTALL_FAILED_INVALID_URI]\n")
					return 1
				}
			} else {
				data, _ = io.ReadAll(c.Stdin)
			}
			return d.install(c, data, "stream")
		case "uninstall":
			return d.uninstall(c, args[1:])
		case "list":
			if len(args) > 1 && args[1] == "packages" {
				return d.listPackages(c, args[2:])
			}
			sub := *c
			sub.Args = append([]string{"pm", "list"}, args[1:]...)
			return cmdPm(&sub)
		case "resolve-activity":
			return cmdResolveActivity(c, args[1:])
		default:
			sub := *c
			sub.Args = append([]string{"pm"}, args...)
			return cmdPm(&sub)
		}
	case "location":
		if len(args) > 0 && args[0] == "is-location-enabled" {
			c.printf("true\n")
		}
		return 0
	case "settings":
		sub := *c
		sub.Args = append([]string{"settings"}, args...)
		return cmdSettings(&sub)
	case "activity":
		sub := *c
		sub.Args = append([]string{"am"}, args...)
		return cmdAm(&sub)
	case "window":
		sub := *c
		sub.Args = append([]string{"wm"}, args...)
		return cmdWm(&sub)
	case "input_method":
		sub := *c
		sub.Args = append([]string{"ime"}, args...)
		return cmdIme(&sub)
	case "wifi":
		if len(args) > 0 && args[0] == "status" {
			if c.d.App.Network() == "wifi" {
				c.printf("Wifi is enabled\nWifi is connected to \"MCloud\"\n")
			} else {
				c.printf("Wifi is disabled\n")
			}
		}
		return 0
	case "connectivity", "statusbar", "overlay", "notification", "uimode", "power", "role", "appops":
		return 0
	}
	return 0
}

// intent is the parsed arguments of am start/startservice/broadcast.
type intent struct {
	action, component, data, category string
	wait, stop                        bool
	extras                            map[string]string
}

func parseIntent(args []string) intent {
	in := intent{extras: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch a {
		case "-a":
			in.action = next()
		case "-n":
			in.component = next()
		case "-d":
			in.data = next()
		case "-c":
			in.category = next()
		case "-W":
			in.wait = true
		case "-S":
			in.stop = true
		case "--user", "-f", "-t", "--display", "--windowingMode", "--activityType", "-p", "--receiver-permission":
			v := next()
			if a == "-p" && in.component == "" {
				in.component = v + "/"
			}
		case "--es", "--ez", "--ei", "--el", "--ef", "--eu", "--ecn", "--eia", "--ela", "--efa", "--esa", "--esn":
			k := next()
			if a == "--esn" {
				in.extras[k] = ""
				continue
			}
			in.extras[k] = next()
		default:
			if !strings.HasPrefix(a, "-") && in.component == "" && strings.Contains(a, "/") {
				in.component = a
			}
		}
	}
	return in
}

func (in intent) pkg() string {
	p, _, _ := strings.Cut(in.component, "/")
	return p
}

func (in intent) String() string {
	var parts []string
	if in.action != "" {
		parts = append(parts, "act="+in.action)
	}
	if in.category != "" {
		parts = append(parts, "cat=["+in.category+"]")
	}
	if in.data != "" {
		parts = append(parts, "dat="+in.data)
	}
	if in.component != "" {
		parts = append(parts, "cmp="+in.component)
	}
	return "Intent { " + strings.Join(parts, " ") + " }"
}

// startApp makes the process of a package run (until am force-stop), like a launched app.
func (d *Device) startApp(pkg string) {
	if len(d.procs.Find(pkg)) > 0 {
		return
	}
	user := "u0_a123"
	_, done := d.procs.Start(context.Background(), user, pkg, pkg)
	_ = done // the process lives until it is killed
}

func cmdAm(c *Cmd) int {
	d := c.d
	if len(c.Args) < 2 {
		return 0
	}
	args := c.Args[2:]
	switch c.Args[1] {
	case "start", "start-activity":
		in := parseIntent(args)
		pkgName := in.pkg()
		if pkgName == "" && in.action == "android.intent.action.VIEW" {
			pkgName = "com.android.chrome"
		}
		if pkgName == "" {
			pkgName = "com.android.launcher3"
		}
		pkg := d.pkgs.Get(pkgName)
		if pkg == nil {
			c.printf("Starting: %s\n", in)
			c.errorf("Error: Activity class {%s} does not exist.\n", in.component)
			return 1
		}
		if in.stop {
			d.procs.KillByName(pkgName, 9)
		}
		d.startApp(pkgName)
		d.App.SetForegroundApp("am", pkgName)
		c.printf("Starting: %s\n", in)
		if in.wait {
			c.printf("Status: ok\nLaunchState: COLD\nActivity: %s\nTotalTime: 120\nWaitTime: 125\nComplete\n", in.component)
		}
		return 0
	case "startservice", "start-service", "start-foreground-service":
		in := parseIntent(args)
		pkgName := in.pkg()
		if d.pkgs.Get(pkgName) == nil {
			c.printf("Starting service: %s\n", in)
			c.printf("Error: Not found; no service started.\n")
			return 0
		}
		c.printf("Starting service: %s\n", in)
		if pkgName == stfPackage {
			d.startSTFService()
		} else {
			d.startApp(pkgName)
		}
		return 0
	case "stopservice", "stop-service":
		in := parseIntent(args)
		if in.pkg() == stfPackage {
			d.procs.KillByName(stfServiceProc, 15)
		}
		c.printf("Stopping service: %s\nService stopped\n", in)
		return 0
	case "force-stop", "kill":
		_, rest := splitFlags(args)
		for _, pkg := range rest {
			if pkg == "0" || pkg == "all" {
				continue
			}
			d.procs.KillByName(pkg, 9)
			if pkg == stfPackage {
				d.procs.KillByName(stfServiceProc, 9)
			}
			if d.App.ForegroundApp() == pkg {
				d.App.SetForegroundApp("am", ui.AndroidPackage)
			}
		}
		return 0
	case "kill-all":
		return 0
	case "broadcast":
		in := parseIntent(args)
		c.printf("Broadcasting: %s\n", in)
		data := ""
		if strings.HasSuffix(in.action, ".clipboard.get") {
			d.mu.Lock()
			data = base64.StdEncoding.EncodeToString([]byte(d.clipboard))
			d.mu.Unlock()
		}
		if strings.HasSuffix(in.action, ".clipboard.set") || strings.HasSuffix(in.action, ".clipboard") {
			if v, ok := in.extras["text"]; ok {
				if dec, err := base64.StdEncoding.DecodeString(v); err == nil {
					v = string(dec)
				}
				d.mu.Lock()
				d.clipboard = v
				d.mu.Unlock()
			}
		}
		c.printf("Broadcast completed: result=-1, data=\"%s\"\n", data)
		return 0
	case "instrument":
		return d.instrument(c, args)
	case "get-current-user":
		c.printf("0\n")
		return 0
	case "stack", "task":
		if len(args) > 0 && args[0] == "list" {
			fg := d.App.ForegroundApp()
			c.printf("RootTask id=1 bounds=[0,0][%d,%d] displayId=0 userId=0\n  taskId=12: %s/.MainActivity bounds=[0,0][%d,%d] userId=0 visible=true\n",
				d.Cfg.Width, d.Cfg.Height, fg, d.Cfg.Width, d.Cfg.Height)
		}
		return 0
	case "get-config":
		c.printf("abi: %s\nconfig: en-rUS-ldltr-sw360dp-w360dp-h640dp-normal-notlong-notround-port-notnight-xhdpi-finger-keysexposed-nokeys-navhidden-nonav-v%d\n",
			d.Cfg.ABI, d.Cfg.SDK)
		return 0
	}
	return 0
}

func (d *Device) instrument(c *Cmd, args []string) int {
	var target string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-e":
			i += 2
		case "-p", "--user", "--abi":
			i++
		default:
			if !strings.HasPrefix(args[i], "-") {
				target = args[i]
			}
		}
	}
	pkgName, runner, _ := strings.Cut(target, "/")
	pkg := d.pkgs.Get(pkgName)
	if pkg == nil || pkg.Instrumentation == "" {
		c.printf("INSTRUMENTATION_STATUS: Error=Unable to find instrumentation info for: ComponentInfo{%s}\nINSTRUMENTATION_STATUS_CODE: -1\n", target)
		c.errorf("android.util.AndroidException: INSTRUMENTATION_FAILED: %s\n", target)
		return 1
	}
	d.App.Log("am", "instrument", "%s", target)
	if pkg.Target == uia2ServerPackage {
		return d.runUIA2(c)
	}
	_ = runner
	c.printf("INSTRUMENTATION_RESULT: stream=\n\nTime: 0.01\n\nOK (0 tests)\n\n\nINSTRUMENTATION_CODE: -1\n")
	return 0
}

func cmdDumpsys(c *Cmd) int {
	d := c.d
	if !d.Booted() {
		c.errorf("Can't find service: %s\n", strings.Join(c.Args[1:], " "))
		return 0
	}
	if len(c.Args) < 2 {
		c.printf("Currently running services:\n  activity\n  battery\n  display\n  input_method\n  package\n  power\n  window\n")
		return 0
	}
	w, h := d.App.Size()
	rot := d.App.Rotation() / 90
	fg := d.App.ForegroundApp()
	act := fg + "/.MainActivity"
	args := c.Args[2:]
	switch c.Args[1] {
	case "display":
		c.printf("DISPLAY MANAGER (dumpsys display)\n  mOnlyCore=false\n  mSafeMode=false\n")
		c.printf("Display Devices: size=1\n  DisplayDeviceInfo{\"Built-in Screen\": uniqueId=\"local:0\", %d x %d, modeId 1, defaultModeId 1, supportedModes [{id=1, width=%d, height=%d, fps=60.0}], density %d, %d.0 x %d.0 dpi, appVsyncOff 1000000, presDeadline 16666666, touch INTERNAL, rotation 0, type INTERNAL, address {port=0}, state ON, FLAG_DEFAULT_DISPLAY, FLAG_ROTATES_WITH_CONTENT, FLAG_SECURE, FLAG_SUPPORTS_PROTECTED_BUFFERS}\n",
			d.Cfg.Width, d.Cfg.Height, d.Cfg.Width, d.Cfg.Height, d.Cfg.Density, d.Cfg.Density, d.Cfg.Density)
		c.printf("    mAdapter=LocalDisplayAdapter\n    mUniqueId=local:0\n    mDisplayToken=android.os.BinderProxy@1\n    mCurrentLayerStack=0\n    mCurrentOrientation=%d\n", rot)
		c.printf("  mDefaultViewport=DisplayViewport{type=INTERNAL, valid=true, isActive=true, displayId=0, uniqueId='local:0', physicalPort=0, orientation=%d, logicalFrame=Rect(0, 0 - %d, %d), physicalFrame=Rect(0, 0 - %d, %d), deviceWidth=%d, deviceHeight=%d}\n",
			rot, w, h, w, h, w, h)
		c.printf("  mBaseDisplayInfo=DisplayInfo{\"Built-in Screen\", displayId 0, FLAG_SECURE, FLAG_SUPPORTS_PROTECTED_BUFFERS, real %d x %d, largest app %d x %d, smallest app %d x %d, mode 1, defaultMode 1, rotation %d, density %d (%d.0 x %d.0) dpi, layerStack 0, type INTERNAL, state ON}\n",
			d.Cfg.Width, d.Cfg.Height, d.Cfg.Width, d.Cfg.Height, d.Cfg.Width, d.Cfg.Height, rot, d.Cfg.Density, d.Cfg.Density, d.Cfg.Density)
	case "window":
		c.printf("WINDOW MANAGER DISPLAY CONTENTS (dumpsys window displays)\n  Display: mDisplayId=0 rootTasks=1\n")
		c.printf("    init=%dx%d %ddpi cur=%dx%d app=%dx%d rng=%dx%d-%dx%d\n", d.Cfg.Width, d.Cfg.Height, d.Cfg.Density, w, h, w, h, min(w, h), min(w, h), max(w, h), max(w, h))
		c.printf("    deferred=false mLayoutNeeded=false mTouchExcludeRegion=SkRegion((0,0,%d,%d))\n", w, h)
		c.printf("  mCurrentRotation=ROTATION_%d\n  mRotation=%d\n  mLastOrientation=-1\n", rot*90, rot)
		c.printf("  mCurrentFocus=Window{1a2b3c u0 %s}\n  mFocusedApp=ActivityRecord{4d5e6f u0 %s t12}\n", act, act)
		c.printf("  mUnrestrictedScreen=(0,0) %dx%d\n  mStableFullscreen=(0,0)-(%d,%d)\n", w, h, w, h)
		c.printf("  mShowingLockscreen=%v mShowingDream=false mDreamingLockscreen=false\n  mScreenOnEarly=true mScreenOnFully=true\n", d.App.Locked())
		c.printf("  isStatusBarKeyguard=%v\n  mInputMethodTarget=Window{1a2b3c u0 %s}\n", d.App.Locked(), act)
		c.printf("  mStatusBar=Window{7a8b9c u0 StatusBar}\n  StatusBar: mFrame=[0,0][%d,48]\n  NavigationBar: mFrame=[0,%d][%d,%d]\n", w, h-96, w, h)
	case "package":
		if len(args) == 0 {
			return 0
		}
		name := args[len(args)-1]
		if p := d.pkgs.Get(name); p != nil {
			c.printf("%s", p.Dumpsys())
		} else {
			c.printf("Unable to find package: %s\n", name)
		}
	case "power":
		c.printf("POWER MANAGER (dumpsys power)\n\nPower Manager State:\n  mWakefulness=Awake\n  mWakefulnessChanging=false\n  mIsPowered=true\n  mPlugType=2\n  mStayOn=true\n  mHoldingWakeLockSuspendBlocker=true\n  mHoldingDisplaySuspendBlocker=true\n  Display Power: state=ON\n")
	case "battery":
		dumpsysBattery(c, args)
	case "input_method":
		d.mu.Lock()
		ime := d.ime
		d.mu.Unlock()
		c.printf("Current Input Method Manager state:\n  mCurMethodId=%s\n  mCurFocusedWindow=Window{1a2b3c u0 %s}\n  mInputShown=false\n  mIsInputViewShown=false\n  mShowRequested=false\n", ime, act)
	case "activity":
		sub := ""
		if len(args) > 0 {
			sub = args[0]
		}
		switch sub {
		case "services":
			c.printf("ACTIVITY MANAGER SERVICES (dumpsys activity services)\n")
			for _, p := range d.procs.List() {
				if len(args) > 1 && p.Name != args[1] {
					continue
				}
				if d.pkgs.Get(p.Name) == nil {
					continue
				}
				c.printf("  User 0 active services:\n  * ServiceRecord{5a6b7c u0 %s/.ForegroundService}\n    intent={cmp=%s/.ForegroundService}\n    packageName=%s\n    processName=%s\n    app=ProcessRecord{8d9e0f %d:%s/u0a123}\n    isForeground=true foregroundId=1 foregroundNoti=Notification(channel=main)\n    createTime=-1m0s startingBgTimeout=--\n",
					p.Name, p.Name, p.Name, p.Name, p.PID, p.Name)
			}
		case "processes":
			c.printf("ACTIVITY MANAGER RUNNING PROCESSES (dumpsys activity processes)\n")
			for _, p := range d.procs.List() {
				c.printf("  *APP* UID 10123 ProcessRecord{abc %d:%s/u0a123}\n", p.PID, p.Name)
			}
		default:
			c.printf("ACTIVITY MANAGER ACTIVITIES (dumpsys activity activities)\nDisplay #0 (activities from top to bottom):\n")
			c.printf("  * Task{abc #12 type=standard A=10123:%s U=0 visible=true mode=fullscreen translucent=false sz=1}\n", fg)
			c.printf("    topResumedActivity=ActivityRecord{4d5e6f u0 %s t12}\n  mResumedActivity: ActivityRecord{4d5e6f u0 %s t12}\n  mFocusedApp=ActivityRecord{4d5e6f u0 %s t12}\n", act, act, act)
			c.printf("  ResumedActivity: ActivityRecord{4d5e6f u0 %s t12}\n", act)
		}
	case "webviewupdate":
		c.printf("Current WebView Update Service state\n  Current WebView package (name, version): (com.google.android.webview, 124.0.6367.219)\n")
	case "SurfaceFlinger":
		c.printf("Display 0 HWC layers:\n")
	case "wifi":
		if d.App.Network() == "wifi" {
			c.printf("Wi-Fi is enabled\nmNetworkInfo [type: WIFI[], state: CONNECTED/CONNECTED, reason: (unspecified), extra: \"MCloud\"]\n")
		} else {
			c.printf("Wi-Fi is disabled\n")
		}
	case "connectivity":
		switch d.App.Network() {
		case "wifi":
			c.printf("Active default network: 100\nNetworkAgentInfo{network{100} handle{1} ni{WIFI CONNECTED extra: \"MCloud\"}}\n")
		case "cellular":
			c.printf("Active default network: 101\nNetworkAgentInfo{network{101} handle{1} ni{MOBILE[LTE] CONNECTED extra: \"internet\"}}\n")
		default:
			c.printf("Active default network: none\n")
		}
	case "meminfo":
		c.printf("Applications Memory Usage (in Kilobytes):\n** MEMINFO in pid 1234 [%s] **\n      TOTAL    42000    30000      200\n", strings.Join(args, " "))
	case "cpuinfo":
		c.printf("Load: 0.1 / 0.2 / 0.3\nCPU usage from 1000ms to 0ms ago:\n  3%% TOTAL: 2%% user + 1%% kernel\n")
	case "gfxinfo":
		c.printf("Applications Graphics Acceleration Info:\nTotal frames rendered: 120\nJanky frames: 0 (0.00%%)\n")
	case "telephony.registry":
		c.printf("  mCallState=0\n  mServiceState=0 0 voice home data home MCloud MCloud 00101\n")
	case "iphonesubinfo":
		c.printf("Phone Subscriber Info:\n  Device ID=000000000000000\n")
	case "deviceidle", "input", "usagestats", "notification", "media.camera", "location":
	}
	return 0
}

func cmdWm(c *Cmd) int {
	d := c.d
	if len(c.Args) < 2 {
		return 0
	}
	switch c.Args[1] {
	case "size":
		w, h := d.Cfg.Width, d.Cfg.Height
		c.printf("Physical size: %dx%d\n", w, h)
	case "density":
		c.printf("Physical density: %d\n", d.Cfg.Density)
	case "user-rotation":
		if len(c.Args) > 3 && c.Args[2] == "lock" {
			if r, err := strconv.Atoi(c.Args[3]); err == nil {
				d.App.SetRotation("wm", r*90)
			}
		} else if len(c.Args) == 2 {
			c.printf("lock %d\n", d.App.Rotation()/90)
		}
	}
	return 0
}

// keycodes of android.view.KeyEvent by name.
var keycodes = map[int]string{
	3: "HOME", 4: "BACK", 19: "DPAD_UP", 20: "DPAD_DOWN", 21: "DPAD_LEFT", 22: "DPAD_RIGHT", 23: "DPAD_CENTER",
	24: "VOLUME_UP", 25: "VOLUME_DOWN", 26: "POWER", 27: "CAMERA", 61: "TAB", 62: " ", 66: "ENTER", 67: "DEL",
	82: "MENU", 84: "SEARCH", 111: "ESCAPE", 112: "FORWARD_DEL", 122: "MOVE_HOME", 123: "MOVE_END",
	164: "VOLUME_MUTE", 187: "APP_SWITCH", 223: "SLEEP", 224: "WAKEUP", 56: ".", 55: ",", 77: "@",
}

// KeyName is the name (or the character) of an Android keycode.
func KeyName(code int) string {
	switch {
	case code >= 7 && code <= 16:
		return string(rune('0' + code - 7))
	case code >= 29 && code <= 54:
		return string(rune('a' + code - 29))
	}
	if n, ok := keycodes[code]; ok {
		return n
	}
	return fmt.Sprintf("KEYCODE_%d", code)
}

func keyByName(name string) int {
	name = strings.TrimPrefix(strings.ToUpper(name), "KEYCODE_")
	if n, err := strconv.Atoi(name); err == nil {
		return n
	}
	for code, n := range keycodes {
		if n == name {
			return code
		}
	}
	if len(name) == 1 {
		ch := name[0]
		if ch >= 'A' && ch <= 'Z' {
			return int(ch-'A') + 29
		}
		if ch >= '0' && ch <= '9' {
			return int(ch-'0') + 7
		}
	}
	return 0
}

func (d *Device) pressKey(source string, code int) {
	switch code {
	case 26, 223:
		d.App.SetLocked(source, !d.App.Locked())
	case 224, 82:
		d.App.SetLocked(source, false)
	}
	d.App.Key(source, KeyName(code))
}

func cmdInput(c *Cmd) int {
	d := c.d
	args := c.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "touchscreen", "touchpad", "keyboard", "mouse", "stylus", "trackball", "dpad", "gamepad", "joystick", "keyboardshortcut":
			args = args[1:]
		}
	}
	if len(args) == 0 {
		c.errorf("Usage: input [<source>] <command> [<arg>...]\n")
		return 1
	}
	num := func(i int) int {
		if i < len(args) {
			v, _ := strconv.ParseFloat(args[i], 64)
			return int(v)
		}
		return 0
	}
	switch args[0] {
	case "tap":
		d.App.Tap("input", num(1), num(2))
	case "swipe", "draganddrop":
		x1, y1, x2, y2 := num(1), num(2), num(3), num(4)
		if abs(x2-x1) < 10 && abs(y2-y1) < 10 {
			d.App.Tap("input", x1, y1)
		} else {
			d.App.Swipe("input", x1, y1, x2, y2)
		}
	case "text":
		if len(args) > 1 {
			d.App.Type("input", strings.ReplaceAll(strings.Join(args[1:], " "), "%s", " "))
		}
	case "keyevent", "keycombination":
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "--") {
				continue
			}
			if code := keyByName(a); code != 0 {
				d.pressKey("input", code)
			}
		}
	case "press", "roll", "motionevent", "scroll":
	default:
		c.errorf("Error: Unknown command: %s\n", args[0])
		return 1
	}
	return 0
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func cmdScreencap(c *Cmd) int {
	_, rest := splitFlags(c.Args[1:])
	w, h := c.d.App.Size()
	png := c.d.Screen.PNG(w, h)
	if len(rest) > 0 {
		_ = c.d.fs.WriteFile(c.abs(rest[0]), adb.SIFREG|0o666, png, time.Now())
		return 0
	}
	_, _ = c.Stdout.Write(png)
	return 0
}

func cmdIme(c *Cmd) int {
	d := c.d
	if len(c.Args) < 2 {
		return 0
	}
	imes := []string{"com.android.inputmethod.latin/.LatinIME", "io.appium.settings/.UnicodeIME", "io.appium.settings/.AppiumIME"}
	switch c.Args[1] {
	case "list":
		flags, _ := splitFlags(c.Args[2:])
		for _, id := range imes {
			pkg, _, _ := strings.Cut(id, "/")
			if d.pkgs.Get(pkg) == nil {
				continue
			}
			if flags["s"] {
				c.printf("%s\n", id)
			} else {
				c.printf("%s:\n  mId=%s mSettingsActivityName=null\n  mIsDefaultResId=0x0\n", id, id)
			}
		}
	case "set":
		if len(c.Args) > 2 {
			d.mu.Lock()
			d.ime = c.Args[2]
			d.settings["secure"]["default_input_method"] = c.Args[2]
			d.mu.Unlock()
			c.printf("Input method %s selected for user #0\n", c.Args[2])
		}
	case "enable", "disable":
		if len(c.Args) > 2 {
			c.printf("Input method %s: %s for user #0\n", c.Args[2], map[string]string{"enable": "now enabled", "disable": "now disabled"}[c.Args[1]])
		}
	case "reset":
		d.mu.Lock()
		d.ime = imes[0]
		d.mu.Unlock()
	}
	return 0
}

func cmdContent(c *Cmd) int {
	c.printf("No result found.\n")
	return 0
}

func cmdService(c *Cmd) int {
	if len(c.Args) > 1 && c.Args[1] == "list" {
		c.printf("Found 4 services:\n0\tactivity: [android.app.IActivityManager]\n1\tpackage: [android.content.pm.IPackageManager]\n2\twindow: [android.view.IWindowManager]\n3\tinput: [android.hardware.input.IInputManager]\n")
		return 0
	}
	if len(c.Args) > 1 && c.Args[1] == "check" {
		c.printf("Service %s: found\n", strings.Join(c.Args[2:], " "))
		return 0
	}
	c.printf("Result: Parcel(00000000 00000001   '........')\n")
	return 0
}

func cmdMonkey(c *Cmd) int {
	args := c.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "-p" && i+1 < len(args) {
			pkg := args[i+1]
			if c.d.pkgs.Get(pkg) == nil {
				c.errorf("** No activities found to run, monkey aborted.\n")
				return 252
			}
			c.d.startApp(pkg)
			c.d.App.SetForegroundApp("monkey", pkg)
		}
	}
	c.printf("  bash arg: -p\nEvents injected: 1\n## Network stats: elapsed time=10ms (0ms mobile, 0ms wifi, 10ms not connected)\n")
	return 0
}

func cmdUiautomator(c *Cmd) int {
	if len(c.Args) > 1 && c.Args[1] == "dump" {
		file := "/sdcard/window_dump.xml"
		if len(c.Args) > 2 && !strings.HasPrefix(c.Args[2], "-") {
			file = c.Args[2]
		}
		if file == "/dev/tty" {
			c.printf("%s\n", ui.AndroidXML(c.d.App.Layout(), c.d.App.Rotation()))
			return 0
		}
		_ = c.d.fs.WriteFile(c.abs(file), adb.SIFREG|0o666, []byte(ui.AndroidXML(c.d.App.Layout(), c.d.App.Rotation())), time.Now())
		c.printf("UI hierchary dumped to: %s\n", file)
	}
	return 0
}

func cmdPs(c *Cmd) int {
	flags, _ := splitFlags(c.Args[1:])
	if flags["--help"] {
		c.printf("usage: ps [-AadefLlnwZ] [-gG GROUP,] [-k FIELD,] [-o FIELD,] [-p PID,] [-t TTY,] [-uU USER,]\n\nList processes.\n\nWhich processes to show (-gGuUpPt selections may be comma separated lists):\n\n-A\tAll\n-a\tHas terminal not session leader\n-d\tAll but session leaders\n-e\tSynonym for -A\n")
		return 0
	}
	c.printf("%s", c.d.procs.PS(flags["A"] || flags["e"] || flags["l"]))
	return 0
}

func cmdPidof(c *Cmd) int {
	var pids []string
	for _, name := range c.Args[1:] {
		if strings.HasPrefix(name, "-") {
			continue
		}
		for _, p := range c.d.procs.Find(name) {
			pids = append(pids, strconv.Itoa(p.PID))
		}
	}
	if len(pids) == 0 {
		return 1
	}
	c.printf("%s\n", strings.Join(pids, " "))
	return 0
}

func cmdPgrep(c *Cmd) int {
	flags, rest := splitFlags(c.Args[1:])
	if len(rest) == 0 {
		return 2
	}
	re, err := regexp.Compile(rest[0])
	if err != nil {
		return 2
	}
	n := 0
	for _, p := range c.d.procs.List() {
		target := p.Name
		if flags["f"] {
			target = p.Cmdline
		}
		ok := re.MatchString(target)
		if flags["x"] {
			ok = target == rest[0]
		}
		if ok {
			if flags["l"] {
				c.printf("%d %s\n", p.PID, p.Name)
			} else {
				c.printf("%d\n", p.PID)
			}
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return 0
}

func parseSignal(s string) int {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "-"), "SIG")
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return map[string]int{"HUP": 1, "INT": 2, "QUIT": 3, "KILL": 9, "TERM": 15, "STOP": 19, "CONT": 18}[strings.ToUpper(s)]
}

func cmdKill(c *Cmd) int {
	sig := 15
	code := 0
	args := c.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-s" && i+1 < len(args):
			i++
			sig = parseSignal(args[i])
		case a == "-l":
			c.printf("HUP INT QUIT ILL TRAP ABRT BUS FPE KILL USR1 SEGV USR2 PIPE ALRM TERM\n")
			return 0
		case strings.HasPrefix(a, "-"):
			sig = parseSignal(a)
		default:
			pid, err := strconv.Atoi(a)
			if err != nil || !c.d.procs.Kill(pid, sig) {
				if pid > 0 && pid < 1000 {
					c.errorf("/system/bin/sh: kill: %s: Operation not permitted\n", a)
				} else {
					c.errorf("/system/bin/sh: kill: %s: No such process\n", a)
				}
				code = 1
			}
		}
	}
	return code
}

func cmdKillall(c *Cmd) int {
	sig := 15
	n := 0
	for _, a := range c.Args[1:] {
		if strings.HasPrefix(a, "-") {
			sig = parseSignal(a)
			continue
		}
		n += c.d.procs.KillByName(a, sig)
	}
	if n == 0 {
		return 1
	}
	return 0
}

// dumpsysBattery prints the battery or changes it like the real one: set level|ac|usb|status N, unplug, reset.
func dumpsysBattery(c *Cmd, args []string) {
	app := c.d.App
	p := app.Power()
	switch {
	case len(args) >= 3 && args[0] == "set":
		n, err := strconv.Atoi(args[2])
		if err != nil {
			c.errorf("Bad value: %s\n", args[2])
			return
		}
		switch args[1] {
		case "level":
			p.Level = n
		case "ac", "usb", "wireless":
			p.Charging = n != 0
		case "status":
			p.Charging = n == 2 || n == 5
		}
		app.SetPower("dumpsys", p)
		return
	case len(args) >= 1 && args[0] == "unplug":
		p.Charging = false
		app.SetPower("dumpsys", p)
		return
	case len(args) >= 1 && args[0] == "reset":
		ui.ApplyConfig(app, c.d.Cfg)
		return
	}
	usb := "false"
	if p.Charging {
		usb = "true"
	}
	c.printf("Current Battery Service state:\n  AC powered: false\n  USB powered: %s\n  Wireless powered: false\n  Max charging current: 500000\n  Max charging voltage: 5000000\n  Charge counter: %d\n  status: %d\n  health: 2\n  present: true\n  level: %d\n  scale: 100\n  voltage: 4200\n  temperature: 250\n  technology: Li-ion\n",
		usb, p.Level*40000, p.Status(), p.Level)
}
