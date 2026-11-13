// Package farm connects the dummies to a farm: it makes the device list of mcloud-agent for a configuration and
// clones the models of a real farm from its STF API.
package farm

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zebrunner/mcloud-dummy/internal/config"
)

// AgentDevice is a device of the `devices` list of mcloud-agent (config/devices.yml).
type AgentDevice struct {
	ID         string `yaml:"id"`
	OS         string `yaml:"os"`
	Name       string `yaml:"name"`
	Connection string `yaml:"connection"`
	Address    string `yaml:"address"`
	WDAPort    int    `yaml:"wda_port,omitempty"`
	MJPEGPort  int    `yaml:"mjpeg_port,omitempty"`
	AppiumPort int    `yaml:"appium_port"`
	MinPort    int    `yaml:"min_port"`
	MaxPort    int    `yaml:"max_port"`
}

// MinPortStep is the smallest block of ports of a device: the Appium port, a spare one and the 8 ports of STF.
const MinPortStep = 10

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// AgentDevices makes the device list of mcloud-agent for the devices of a configuration: Android over adb connect to
// host:port (android-sim), iOS over the WDA and MJPEG ports of host (proxy). Every device takes a block of step
// ports from portStart: the Appium port, then min_port-max_port of STF.
func AgentDevices(devices []config.Device, host string, portStart, step int) ([]AgentDevice, error) {
	if step < MinPortStep {
		return nil, fmt.Errorf("the port step must be at least %d, got %d", MinPortStep, step)
	}
	if last := portStart + step*len(devices); portStart < 1024 || last > 65535 {
		return nil, fmt.Errorf("the ports %d-%d are out of 1024-65535", portStart, last)
	}
	out := make([]AgentDevice, 0, len(devices))
	for i, d := range devices {
		base := portStart + step*i
		a := AgentDevice{ID: d.Serial, OS: d.Platform, Name: strings.Trim(unsafeName.ReplaceAllString(d.Name, "_"), "_"),
			AppiumPort: base + 1, MinPort: base + 3, MaxPort: base + MinPortStep}
		if d.Platform == "ios" {
			a.Connection, a.Address, a.WDAPort, a.MJPEGPort = "proxy", host, d.WDAPort, d.MJPEGPort
		} else {
			a.Connection, a.Address = "android-sim", fmt.Sprintf("%s:%d", host, d.Port)
		}
		out = append(out, a)
	}
	return out, nil
}

// MarshalDevices is the `devices:` section of config/devices.yml of mcloud-agent.
func MarshalDevices(devices []AgentDevice) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(map[string][]AgentDevice{"devices": devices}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReplaceDevices replaces the `devices:` section of config/devices.yml of mcloud-agent and keeps the rest of the file, the
// comments of the next key included.
func ReplaceDevices(vars, section []byte) ([]byte, error) {
	lines := strings.SplitAfter(string(vars), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "devices:") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("no devices: section")
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if l := lines[i]; strings.TrimSpace(l) != "" && l[0] != ' ' && l[0] != '-' && l[0] != '#' {
			end = i
			break
		}
	}
	for end < len(lines) && end > start+1 && (strings.TrimSpace(lines[end-1]) == "" || lines[end-1][0] == '#') {
		end--
	}
	var out strings.Builder
	out.WriteString(strings.Join(lines[:start], ""))
	out.Write(section)
	if end < len(lines) {
		out.WriteString("\n")
		out.WriteString(strings.TrimLeft(strings.Join(lines[end:], ""), "\n"))
	}
	return []byte(out.String()), nil
}
