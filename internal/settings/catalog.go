// Package settings owns human settings, wire conversions and readback comparison.
// It performs no I/O and has no model-specific dependency.
package settings

import "time"

type Spec struct {
	Name             string `json:"name"`
	HEX              byte   `json:"hex_key,omitempty"`
	JSON             string `json:"json_key,omitempty"`
	Kind             string `json:"kind"`
	Unit             string `json:"unit,omitempty"`
	Min, Max         int64
	Scale            int64
	JSONScale        int64
	JSONMin, JSONMax int64
	Enum             []string `json:"values,omitempty"`
	Note             string   `json:"note,omitempty"`
}

// These are default accepted bounds, including firmware compatibility exceptions.
// wireBounds separately defines the limits that can never be bypassed.
// HEX offsets use the signed interpretation already implemented by the decoder.
var Catalog = []Spec{
	{Name: "reporting.upload_interval", HEX: 0x04, JSON: "report_interval", Kind: "duration", Unit: "m", Min: 1, Max: 65535, JSONMin: 11, JSONMax: 2147483647},
	{Name: "reporting.record_interval", HEX: 0x05, JSON: "collect_interval", Kind: "duration", Unit: "s", Min: 1, Max: 65535, JSONMin: 10, JSONMax: 2147483647, Note: "CGDN1 compatibility: 10s is observed although the specification says >10s"},
	{Name: "reporting.event_repeat_interval", HEX: 0x1b, Kind: "duration", Unit: "s", Min: 0, Max: 65535},
	{Name: "reporting.delay", HEX: 0x37, Kind: "duration", Unit: "s", Min: 0, Max: 65535},
	{Name: "reporting.high_frequency_interval", HEX: 0x7b, Kind: "duration", Unit: "s", Min: 0, Max: 64800, Note: "Specification maximum 18h; zero semantics unconfirmed"},
	{Name: "reporting.require_ack", JSON: "need_ack", Kind: "bool", Note: "Device preference only; does not enable this program's automatic replies"},
	{Name: "display.temperature_unit", HEX: 0x19, JSON: "temperature_unit", Kind: "enum", Enum: []string{"celsius", "fahrenheit"}},
	{Name: "display.clock_format", HEX: 0x3e, JSON: "is_12_hour_mode", Kind: "enum", Enum: []string{"24h", "12h"}},
	{Name: "display.led_enabled", HEX: 0x63, Kind: "bool"},
	{Name: "display.tvoc_enabled", HEX: 0x62, Kind: "bool"},
	{Name: "display.off_after", JSON: "display_off_time", Kind: "duration", Unit: "s", JSONMin: 0, JSONMax: 300, Note: "0s means never"},
	{Name: "display.rotate_interval", JSON: "auto_slideing_time", Kind: "duration", Unit: "s", JSONMin: 0, JSONMax: 120, Note: "0..60s documented; 120s also accepted because L reports it; 61..119s not validated"},
	{Name: "display.screensaver", JSON: "screensaver_type", Kind: "enum", Enum: []string{"rotating_readings", "current_reading", "time_and_rotating_readings", "time_and_current_reading"}},
	{Name: "display.pages", JSON: "page_sequence", Kind: "pages", Note: "Ordered sensor names; repeats preserved; temperature maps to temp"},
	{Name: "display.night_mode.start", JSON: "night_mode_start_time", Kind: "time"},
	{Name: "display.night_mode.end", JSON: "night_mode_end_time", Kind: "time"},
	{Name: "clock.timezone", HEX: 0x88, JSON: "timezone", Kind: "timezone", Note: "Fixed UTC offset +/-HH:MM, exact 0.1h steps; no automatic DST"},
	{Name: "clock.sntp_enabled", HEX: 0x44, Kind: "bool"},
	{Name: "clock.sntp_server", HEX: 0x43, Kind: "string"},
	{Name: "power.battery_off_after", HEX: 0x3d, JSON: "power_off_time", Kind: "duration", Unit: "m", Min: 0, Max: 65535, JSONMin: 0, JSONMax: 60, Note: "0 means never; HEX zero interpretation observed, not explicit in vendor table"},
	{Name: "co2.measurement_interval", HEX: 0x3b, JSON: "co2_sampling_interval", JSONMin: 1, JSONMax: 2147483647, Note: "JSON seconds per legacy Python protocol definitions; not in current Lite table", Kind: "duration", Unit: "m", Min: 1, Max: 65535},
	{Name: "co2.automatic_calibration", HEX: 0x40, JSON: "co2_asc", Kind: "bool", Note: "JSON meaning follows vendor name and observed report; write behavior unverified"},
	{Name: "co2.indicator", HEX: 0x3c, Kind: "range", Scale: 1, Min: 0, Max: 65535, Note: "yellow_min_ppm and yellow_max_ppm, both inclusive"},
	{Name: "calibration.co2.offset_ppm", HEX: 0x45, JSON: "co2_offset", Kind: "number", Scale: 1, JSONScale: 1, Min: -32768, Max: 32767},
	{Name: "calibration.co2.offset_percent", HEX: 0x3f, JSON: "co2_zoom", Kind: "number", Scale: 10, JSONScale: 10, Min: -32768, Max: 32767},
	{Name: "calibration.temperature.offset_c", HEX: 0x46, JSON: "temperature_offset", Kind: "number", Scale: 10, JSONScale: 100, Min: -32768, Max: 32767},
	{Name: "calibration.temperature.offset_percent", HEX: 0x47, JSON: "temperature_zoom", Kind: "number", Scale: 10, JSONScale: 1, Min: -32768, Max: 32767, Note: "JSON spec calls temperature_zoom percentage without x10; unlike other zoom fields; not live verified"},
	{Name: "calibration.humidity.offset_percent_rh", HEX: 0x48, JSON: "humidity_offset", Kind: "number", Scale: 10, JSONScale: 10, Min: -32768, Max: 32767},
	{Name: "calibration.humidity.offset_percent", HEX: 0x49, JSON: "humidity_zoom", Kind: "number", Scale: 10, JSONScale: 10, Min: -32768, Max: 32767},
	{Name: "calibration.pm25.offset_ug_m3", HEX: 0x4b, JSON: "pm25_offset", Kind: "number", Scale: 1, JSONScale: 1, Min: -32768, Max: 32767},
	{Name: "calibration.pm25.offset_percent", HEX: 0x4c, JSON: "pm25_zoom", Kind: "number", Scale: 10, JSONScale: 10, Min: -32768, Max: 32767},
	{Name: "calibration.pm10.offset_ug_m3", HEX: 0x4d, JSON: "pm10_offset", Kind: "number", Scale: 1, JSONScale: 1, Min: -32768, Max: 32767},
	{Name: "calibration.pm10.offset_percent", HEX: 0x4e, JSON: "pm10_zoom", Kind: "number", Scale: 10, JSONScale: 10, Min: -32768, Max: 32767},
	{Name: "pm.measurement_interval", JSON: "pm_sampling_interval", Kind: "duration", Unit: "s", JSONMin: 1, JSONMax: 2147483647, Note: "Seconds per legacy Python protocol definitions; present in L reports; current vendor table omits units"},
	{Name: "indicators.temperature_thresholds_c", JSON: "temp_led_th", Kind: "thresholds", JSONScale: 100, JSONMin: -2147483648, JSONMax: 2147483647, Note: "x100 per Python reference; color/boundary semantics unverified"},
	{Name: "indicators.humidity_thresholds_percent_rh", JSON: "humi_led_th", Kind: "thresholds", JSONScale: 100, JSONMin: 0, JSONMax: 2147483647, Note: "x100 per Python reference; color/boundary semantics unverified"},
	{Name: "indicators.co2_thresholds_ppm", JSON: "co2_led_th", Kind: "thresholds", JSONScale: 1, JSONMin: 0, JSONMax: 2147483647, Note: "Ordered thresholds; no assumed color or inclusivity"},
	{Name: "indicators.pm25_thresholds_ug_m3", JSON: "pm25_led_th", Kind: "thresholds", JSONScale: 1, JSONMin: 0, JSONMax: 2147483647, Note: "Ordered thresholds; no assumed color or inclusivity"},
	{Name: "indicators.pm10_thresholds_ug_m3", JSON: "pm10_led_th", Kind: "thresholds", JSONScale: 1, JSONMin: 0, JSONMax: 2147483647, Note: "Ordered thresholds; no assumed color or inclusivity"},
}

func (s Spec) supported(kind string) bool {
	return kind == "hex" && s.HEX != 0 || kind == "json" && s.JSON != ""
}
func (s Spec) scale(kind string) int64 {
	if kind == "json" {
		return s.JSONScale
	}
	return s.Scale
}
func (s Spec) bounds(kind string) (int64, int64) {
	if kind == "json" {
		if s.Kind == "number" {
			return -2147483648, 2147483647
		}
		return s.JSONMin, s.JSONMax
	}
	return s.Min, s.Max
}
func (s Spec) wireBounds(kind string) (int64, int64) {
	if kind == "json" {
		return -2147483648, 2147483647
	}
	switch s.Kind {
	case "bool", "enum":
		return 0, 255
	case "number", "timezone":
		return -32768, 32767
	default:
		return 0, 65535
	}
}
func (s Spec) durationUnit(kind string) time.Duration {
	if kind == "json" && s.JSON != "power_off_time" {
		return time.Second
	}
	if s.Unit == "m" {
		return time.Minute
	}
	return time.Second
}
