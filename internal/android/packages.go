package android

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shogo82148/androidbinary/apk"
)

// Package is an installed app.
type Package struct {
	Name            string
	VersionCode     int
	VersionName     string
	Path            string
	System          bool
	Instrumentation string // runner class
	Target          string // instrumentation target package
	Launcher        string // main activity
	Permissions     []string
	Granted         map[string]bool
	InstalledAt     time.Time
}

// Packages is the package manager of a device.
type Packages struct {
	mu   sync.Mutex
	pkgs map[string]*Package
}

var systemPackages = []string{
	"android", "com.android.systemui", "com.android.settings", "com.android.shell", "com.android.phone",
	"com.android.providers.settings", "com.android.launcher3", "com.android.chrome", "com.google.android.webview",
	"com.android.inputmethod.latin", "com.android.vending", "com.google.android.gms",
}

func newPackages() *Packages {
	p := &Packages{pkgs: map[string]*Package{}}
	for _, name := range systemPackages {
		p.pkgs[name] = &Package{Name: name, VersionCode: 1, VersionName: "1.0", System: true,
			Path: "/system/app/" + name + "/" + name + ".apk", Granted: map[string]bool{}}
	}
	p.pkgs["com.android.chrome"].VersionName = "124.0.6367.219"
	p.pkgs["com.android.chrome"].VersionCode = 636721933
	p.pkgs["com.google.android.webview"].VersionName = "124.0.6367.219"
	p.pkgs["com.google.android.webview"].VersionCode = 636721933
	p.pkgs["com.android.chrome"].Launcher = "com.google.android.apps.chrome.Main"
	p.pkgs["com.android.settings"].Launcher = ".Settings"
	p.pkgs["com.android.launcher3"].Launcher = ".Launcher"
	p.pkgs[uiPackage()] = &Package{Name: uiPackage(), VersionCode: 1, VersionName: "1.0",
		Path: "/data/app/" + uiPackage() + "-1/base.apk", Launcher: ".MainActivity", Granted: map[string]bool{}}
	return p
}

func uiPackage() string { return "com.zebrunner.dummy" }

// Get returns an installed package.
func (p *Packages) Get(name string) *Package {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pkgs[name]
}

// List returns the packages sorted by name.
func (p *Packages) List() []*Package {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*Package, 0, len(p.pkgs))
	for _, pkg := range p.pkgs {
		out = append(out, pkg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ByPath returns the package installed from the given apk path.
func (p *Packages) ByPath(path string) *Package {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pkg := range p.pkgs {
		if pkg.Path == path {
			return pkg
		}
	}
	return nil
}

// Install parses the manifest of an APK and installs (or replaces) its package.
func (p *Packages) Install(data []byte) (*Package, error) {
	a, err := apk.OpenZipReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("INSTALL_PARSE_FAILED_NOT_APK: %v", err)
	}
	defer a.Close()
	m := a.Manifest()
	name := a.PackageName()
	if name == "" {
		return nil, fmt.Errorf("INSTALL_PARSE_FAILED_MANIFEST_MALFORMED: no package name")
	}
	pkg := &Package{Name: name, Granted: map[string]bool{}, InstalledAt: time.Now()}
	if v, err := m.VersionCode.Int32(); err == nil {
		pkg.VersionCode = int(v)
	}
	if v, err := m.VersionName.String(); err == nil {
		pkg.VersionName = v
	}
	if v, err := m.Instrument.Name.String(); err == nil && v != "" {
		pkg.Instrumentation = v
		pkg.Target, _ = m.Instrument.Target.String()
	}
	if act, err := a.MainActivity(); err == nil {
		pkg.Launcher = act
	}
	for _, perm := range m.UsesPermissions {
		if n, err := perm.Name.String(); err == nil {
			pkg.Permissions = append(pkg.Permissions, n)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 1
	if old, ok := p.pkgs[name]; ok {
		if old.System {
			pkg.System = true
		}
		if strings.HasSuffix(old.Path, "-1/base.apk") {
			n = 2
		}
	}
	pkg.Path = fmt.Sprintf("/data/app/%s-%d/base.apk", name, n)
	p.pkgs[name] = pkg
	return pkg, nil
}

// Uninstall removes a package; false when it is not installed or a system package.
func (p *Packages) Uninstall(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pkg, ok := p.pkgs[name]
	if !ok || pkg.System {
		return false
	}
	delete(p.pkgs, name)
	return true
}

// Grant records a granted permission.
func (p *Packages) Grant(name, perm string, granted bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pkg, ok := p.pkgs[name]
	if !ok {
		return false
	}
	pkg.Granted[perm] = granted
	return true
}

// Instrumentations lists "instrumentation:<pkg>/<runner> (target=<target>)" lines.
func (p *Packages) Instrumentations() []string {
	var out []string
	for _, pkg := range p.List() {
		if pkg.Instrumentation == "" {
			continue
		}
		runner := pkg.Instrumentation
		if strings.HasPrefix(runner, ".") {
			runner = pkg.Name + runner
		}
		out = append(out, fmt.Sprintf("instrumentation:%s/%s (target=%s)", pkg.Name, runner, pkg.Target))
	}
	return out
}

// Dumpsys is the "dumpsys package <name>" output that appium-adb parses.
func (pkg *Package) Dumpsys() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Activity Resolver Table:\n  Non-Data Actions:\n")
	if pkg.Launcher != "" {
		act := pkg.Launcher
		if strings.HasPrefix(act, ".") {
			act = pkg.Name + act
		}
		fmt.Fprintf(&b, "      android.intent.action.MAIN:\n        1234abcd %s/%s filter 5678ef00\n", pkg.Name, act)
	}
	fmt.Fprintf(&b, "\nPackages:\n  Package [%s] (1a2b3c4d):\n", pkg.Name)
	fmt.Fprintf(&b, "    userId=10123\n    pkg=Package{1a2b3c4d %s}\n", pkg.Name)
	fmt.Fprintf(&b, "    codePath=%s\n", strings.TrimSuffix(pkg.Path, "/base.apk"))
	fmt.Fprintf(&b, "    versionCode=%d minSdk=24 targetSdk=34\n", pkg.VersionCode)
	fmt.Fprintf(&b, "    versionName=%s\n", pkg.VersionName)
	fmt.Fprintf(&b, "    flags=[ HAS_CODE ALLOW_CLEAR_USER_DATA ]\n")
	ts := pkg.InstalledAt
	if ts.IsZero() {
		ts = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	fmt.Fprintf(&b, "    timeStamp=%s\n    firstInstallTime=%s\n    lastUpdateTime=%s\n",
		ts.Format("2006-01-02 15:04:05"), ts.Format("2006-01-02 15:04:05"), ts.Format("2006-01-02 15:04:05"))
	if len(pkg.Permissions) > 0 {
		b.WriteString("    requested permissions:\n")
		for _, perm := range pkg.Permissions {
			fmt.Fprintf(&b, "      %s\n", perm)
		}
		b.WriteString("    install permissions:\n")
		b.WriteString("    runtime permissions:\n")
		for _, perm := range pkg.Permissions {
			fmt.Fprintf(&b, "      %s: granted=%v\n", perm, pkg.Granted[perm])
		}
	}
	b.WriteString("    User 0: ceDataInode=0 installed=true hidden=false suspended=false stopped=false notLaunched=false enabled=0\n")
	return b.String()
}
