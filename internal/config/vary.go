package config

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// fieldByYAML returns the field of a device with the YAML name.
func fieldByYAML(d *Device, name string) (reflect.Value, bool) {
	v := reflect.ValueOf(d).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0] == name {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// overlay copies the fields of src that are set (not zero) over dst.
func overlay(dst *Device, src Device) {
	dv := reflect.ValueOf(dst).Elem()
	sv := reflect.ValueOf(src)
	for i := 0; i < sv.NumField(); i++ {
		if !sv.Field(i).IsZero() {
			dv.Field(i).Set(sv.Field(i))
		}
	}
}

var (
	rangeRe  = regexp.MustCompile(`^\s*(-?\d+(?:\.\d+)?)\s*-\s*(-?\d+(?:\.\d+)?)\s*$`)
	screenRe = regexp.MustCompile(`^(\d+)x(\d+)(?:@(\d+))?$`)
	// the fields a vary never sets: they make the entry, not a device
	notVaried = map[string]bool{"platform": true, "count": true, "vary": true, "pick": true, "seed": true, "serial": true,
		"port": true, "wdaPort": true, "mjpegPort": true}
)

// pick is the value of a vary key for the i-th of count devices of an entry.
func pick(entry Device, key string, i, count int) (any, error) {
	if notVaried[key] {
		return nil, fmt.Errorf("vary %s: the field is not varied", key)
	}
	spec := entry.Vary[key]
	rng := rand.New(rand.NewPCG(uint64(entry.Seed), uint64(i)<<32|uint64(hash(key))))
	switch v := spec.(type) {
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("vary %s: no values", key)
		}
		if entry.Pick == "cycle" {
			return v[i%len(v)], nil
		}
		return v[rng.IntN(len(v))], nil
	case string:
		m := rangeRe.FindStringSubmatch(v)
		if m == nil {
			return v, nil
		}
		lo, _ := strconv.ParseFloat(m[1], 64)
		hi, _ := strconv.ParseFloat(m[2], 64)
		if hi < lo {
			return nil, fmt.Errorf("vary %s: the range %q goes down", key, v)
		}
		var f float64
		if entry.Pick == "cycle" && count > 1 {
			f = lo + (hi-lo)*float64(i)/float64(count-1)
		} else {
			f = lo + (hi-lo)*rng.Float64()
		}
		if !strings.Contains(m[1]+m[2], ".") {
			return int(f + 0.5), nil
		}
		return f, nil
	}
	return spec, nil
}

func hash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// setField sets a field of a device by its YAML name from a value of YAML; `screen` is the size and the density
// (Android) or the scale (iOS): "1080x2400@420".
func setField(d *Device, name string, value any) error {
	if name == "screen" {
		m := screenRe.FindStringSubmatch(fmt.Sprint(value))
		if m == nil {
			return fmt.Errorf("a screen is <width>x<height>[@<density or scale>], got %v", value)
		}
		d.Width, _ = strconv.Atoi(m[1])
		d.Height, _ = strconv.Atoi(m[2])
		if m[3] != "" {
			n, _ := strconv.Atoi(m[3])
			if d.Platform == "ios" {
				d.Scale = n
			} else {
				d.Density = n
			}
		}
		return nil
	}
	f, ok := fieldByYAML(d, name)
	if !ok || notVaried[name] || name == "profile" {
		return fmt.Errorf("unknown field %q", name)
	}
	s := fmt.Sprint(value)
	set := func(v any) {
		f.Set(reflect.ValueOf(v))
	}
	switch f.Interface().(type) {
	case string:
		set(s)
	case int:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("a number is expected, got %q", s)
		}
		set(int(n))
	case float64:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("a number is expected, got %q", s)
		}
		set(n)
	case *int:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("a number is expected, got %q", s)
		}
		set(ptr(int(n)))
	case *bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("true or false is expected, got %q", s)
		}
		set(ptr(b))
	case time.Duration:
		dur, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		set(dur)
	default:
		return fmt.Errorf("the field %q is not varied", name)
	}
	return nil
}
