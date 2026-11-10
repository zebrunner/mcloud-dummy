// Command mcloud-dummy emulates Android and iOS devices for testing the Zebrunner MCloud device farm.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zebrunner/mcloud-dummy/internal/android"
	"github.com/zebrunner/mcloud-dummy/internal/config"
	"github.com/zebrunner/mcloud-dummy/internal/control"
	"github.com/zebrunner/mcloud-dummy/internal/farm"
	"github.com/zebrunner/mcloud-dummy/internal/ios"
)

// Exit codes of the container (CONVENTIONS.md#exit-codes): a restart does not fix an invalid configuration (0), it may
// fix a port that is still in use (1).
const (
	exitConfig  = 0
	exitRestart = 1
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "farm-devices":
			os.Exit(farmDevices(os.Args[2:]))
		case "clone":
			os.Exit(clone(os.Args[2:]))
		}
	}
	configPath := flag.String("config", os.Getenv("DUMMY_CONFIG"), "YAML file of the devices (env DUMMY_CONFIG, see examples/devices.yml)")
	androidCount := flag.Int("android", 0, "without a configuration: the number of Android devices (adb ports from 15555)")
	iosCount := flag.Int("ios", 0, "without a configuration: the number of iOS devices (WDA/MJPEG ports from 18100)")
	host := flag.String("host", os.Getenv("DUMMY_HOST"), "the address of this host the farm connects to (env DUMMY_HOST, default: the hostname)")
	controlAddr := flag.String("control", os.Getenv("DUMMY_CONTROL"), "the listen address of the control API (env DUMMY_CONTROL, default :8800)")
	logLevel := flag.String("log-level", envOr("LOGGER_LEVEL", "INFO"), "DEBUG, INFO, WARN or ERROR (env LOGGER_LEVEL)")
	healthcheck := flag.Bool("healthcheck", false, "checks the control API of a running mcloud-dummy and exits (the HEALTHCHECK of the image)")
	flag.Parse()

	if *healthcheck {
		os.Exit(checkHealth(*controlAddr))
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(*logLevel))); err != nil {
		fmt.Fprintln(os.Stderr, "LOGGER_LEVEL must be DEBUG, INFO, WARN or ERROR:", err)
		os.Exit(exitConfig)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := loadConfig(*configPath, *androidCount, *iosCount)
	if err != nil {
		log.Error("invalid configuration, a restart does not fix it", "err", err)
		os.Exit(exitConfig)
	}
	if *host != "" {
		cfg.Host = *host
	}
	if *controlAddr != "" {
		cfg.Control = *controlAddr
	}

	srv := &control.Server{Host: cfg.Host}
	for _, dc := range cfg.Devices {
		switch dc.Platform {
		case "android":
			d := android.New(dc, log)
			if err := d.Start(); err != nil {
				log.Error("device can't start", "serial", dc.Serial, "err", err)
				os.Exit(exitRestart)
			}
			srv.Android = append(srv.Android, d)
			log.Info("android device", "serial", dc.Serial, "model", dc.Model, "adb", fmt.Sprintf("%s:%d", cfg.Host, dc.Port))
		case "ios":
			d := ios.New(dc, cfg.Host, log)
			if err := d.Start(); err != nil {
				log.Error("device can't start", "serial", dc.Serial, "err", err)
				os.Exit(exitRestart)
			}
			srv.IOS = append(srv.IOS, d)
			log.Info("ios device", "serial", dc.Serial, "model", dc.Model,
				"wda", fmt.Sprintf("%s:%d", cfg.Host, dc.WDAPort), "mjpeg", fmt.Sprintf("%s:%d", cfg.Host, dc.MJPEGPort))
		}
	}

	httpSrv := &http.Server{Addr: cfg.Control, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("control API", "listen", cfg.Control)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("control API failed", "err", err)
			os.Exit(exitRestart)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Info("stopping")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdown)
	for _, d := range srv.Android {
		d.Stop()
	}
	for _, d := range srv.IOS {
		d.Stop()
	}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func loadConfig(path string, androidCount, iosCount int) (*config.Config, error) {
	if path != "" && androidCount == 0 && iosCount == 0 {
		return config.Load(path)
	}
	if androidCount == 0 && iosCount == 0 {
		androidCount, iosCount = 1, 1
	}
	yaml := "devices:\n"
	if androidCount > 0 {
		yaml += fmt.Sprintf("  - platform: android\n    count: %d\n", androidCount)
	}
	if iosCount > 0 {
		yaml += fmt.Sprintf("  - platform: ios\n    count: %d\n", iosCount)
	}
	return config.Parse([]byte(yaml))
}

// checkHealth asks /healthz of the control API on this host.
func checkHealth(addr string) int {
	if addr == "" {
		addr = ":8800"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return exitRestart
	}
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitRestart
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return exitRestart
	}
	return 0
}

// farmDevices prints the device list of mcloud-agent for a configuration, or writes it into the vars of the agent.
func farmDevices(args []string) int {
	fs := flag.NewFlagSet("farm-devices", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: mcloud-dummy farm-devices -config devices.yml -host mcloud-dummy [-port-start 8600] [-vars roles/devices/vars/main.yml]")
		fmt.Fprintln(fs.Output(), "prints the devices: list of mcloud-agent for the dummies (android-sim and proxy connections)")
		fs.PrintDefaults()
	}
	configPath := fs.String("config", os.Getenv("DUMMY_CONFIG"), "YAML file of the devices (env DUMMY_CONFIG)")
	host := fs.String("host", os.Getenv("DUMMY_HOST"), "the address of mcloud-dummy the connectors reach (env DUMMY_HOST)")
	portStart := fs.Int("port-start", 8600, "the first port of the Appium and STF ports of the devices")
	portStep := fs.Int("port-step", 20, fmt.Sprintf("the ports per device (at least %d)", farm.MinPortStep))
	vars := fs.String("vars", "", "the vars of mcloud-agent to replace the devices: section of, in place")
	_ = fs.Parse(args)
	if *configPath == "" || *host == "" {
		fs.Usage()
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	devices, err := farm.AgentDevices(cfg.Devices, *host, *portStart, *portStep)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	section, err := farm.MarshalDevices(devices)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *vars == "" {
		_, _ = os.Stdout.Write(section)
		return 0
	}
	old, err := os.ReadFile(*vars)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	updated, err := farm.ReplaceDevices(old, section)
	if err == nil {
		err = os.WriteFile(*vars, updated, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, *vars+":", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "%s: %d devices\n", *vars, len(devices))
	return 0
}

// clone makes a configuration with the models of the devices of a real farm, read from its STF API.
func clone(args []string) int {
	fs := flag.NewFlagSet("clone", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: STF_TOKEN=... mcloud-dummy clone -stf https://farm.example.com [-platform android] [-count 20] [-o fleet.yml]")
		fmt.Fprintln(fs.Output(), "writes a configuration with a profile per model of the real devices (the token is an access token of STF)")
		fs.PrintDefaults()
	}
	stf := fs.String("stf", os.Getenv("STF_URL"), "the URL of STF (env STF_URL)")
	insecure := fs.Bool("insecure", false, "do not verify the TLS certificate of STF")
	platform := fs.String("platform", "", "android or ios; both by default")
	count := fs.Int("count", 0, "the devices per platform; as many as the real ones by default")
	seed := fs.Int64("seed", 1, "the seed of the random picks of the configuration")
	output := fs.String("o", "", "the output file; stdout by default")
	_ = fs.Parse(args)
	token := os.Getenv("STF_TOKEN")
	if *stf == "" || token == "" {
		fs.Usage()
		return 2
	}
	if *platform != "" && *platform != "android" && *platform != "ios" {
		fmt.Fprintln(os.Stderr, "-platform must be android or ios")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	devices, err := farm.FetchDevices(ctx, *stf, token, *insecure)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out, err := farm.Clone(devices, farm.CloneOptions{Source: *stf, Platform: *platform, Count: *count, Seed: *seed})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := config.Parse(out); err != nil {
		fmt.Fprintln(os.Stderr, "the clone is not a valid configuration:", err)
		return 1
	}
	if *output == "" {
		_, _ = os.Stdout.Write(out)
		return 0
	}
	if err := os.WriteFile(*output, out, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "%s: %d real devices\n", *output, len(devices))
	return 0
}
