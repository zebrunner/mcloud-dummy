package ui

import "github.com/zebrunner/mcloud-dummy/internal/config"

// ApplyConfig sets the initial state of a device from its configuration: rotation, battery, network.
func ApplyConfig(app *App, cfg config.Device) {
	app.SetRotation("config", cfg.Rotation)
	p := Power{Level: 100, Charging: true}
	if cfg.Battery != nil {
		p.Level = *cfg.Battery
	}
	if cfg.Charging != nil {
		p.Charging = *cfg.Charging
	}
	app.SetPower("config", p)
	if cfg.Network != "" {
		app.SetNetwork("config", cfg.Network)
	}
}
