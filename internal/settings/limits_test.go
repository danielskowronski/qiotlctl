package settings

import (
	"encoding/hex"
	"testing"
)

func TestIgnoreOfficialLimitsWireValues(t *testing.T) {
	opt := Options{IgnoreOfficialLimits: true}
	for _, tc := range []struct{ kind, input, wire string }{
		{"hex", "reporting: {high_frequency_interval: 65535s}", "ffff"},
		{"hex", "reporting: {upload_interval: 0s}", "0000"},
		{"hex", "display: {temperature_unit: 255}", "ff"},
		{"hex", "display: {led_enabled: 2}", "02"},
		{"hex", "clock: {timezone: '+20:00'}", "c800"},
		{"hex", "clock: {timezone: -32768}", "0080"},
		{"hex", "clock: {timezone: '-3276:48'}", "0080"},
		{"hex", "clock: {sntp_server: ''}", ""},
		{"hex", "co2: {indicator: {yellow_min_ppm: 2000, yellow_max_ppm: 1000}}", "d007e803"},
		{"json", "reporting: {upload_interval: 1s}", "1"},
		{"json", "reporting: {record_interval: -2147483648s}", "-2147483648"},
		{"json", "display: {off_after: 301s}", "301"},
		{"json", "display: {rotate_interval: 90s}", "90"},
		{"json", "display: {rotate_interval: 121s}", "121"},
		{"json", "power: {battery_off_after: 61m}", "61"},
		{"json", "display: {screensaver: 4}", "4"},
		{"json", "display: {clock_format: -2147483648}", "-2147483648"},
		{"json", "reporting: {require_ack: 2147483647}", "2147483647"},
		{"json", "display: {temperature_unit: K}", `"K"`},
		{"json", "display: {pages: [temperature, unknown]}", `"temp,unknown"`},
		{"json", "display: {pages: []}", `""`},
		{"json", "display: {night_mode: {start: 1440}}", "1440"},
		{"json", "display: {night_mode: {start: '24:00'}}", "1440"},
		{"json", "display: {night_mode: {end: '-01:00'}}", "-60"},
		{"json", "clock: {timezone: 2147483647}", "2147483647"},
		{"json", "indicators: {co2_thresholds_ppm: [1000, -1, 1000]}", `"1000,-1,1000"`},
		{"json", "indicators: {humidity_thresholds_percent_rh: []}", `""`},
		{"hex", "threshold_alarms: [{sensor: co2, condition: above, threshold: 1400, unit: ppm, repeat: 255, start: 4294967295, end: 1440}]", "ffffffffffa00500007805"},
	} {
		t.Run(tc.kind+"/"+tc.input, func(t *testing.T) {
			v := values(t, tc.input)
			if _, err := Build(tc.kind, v); err == nil {
				t.Fatal("experimental input accepted without opt-in")
			}
			p, err := Build(tc.kind, v, opt)
			if err != nil || len(p.Fields) != 1 {
				t.Fatal(p, err)
			}
			got := canonical(p.Fields[0].JSON)
			if tc.kind == "hex" {
				got = hex.EncodeToString(p.Fields[0].HEX.Value)
			}
			if got != tc.wire {
				t.Fatalf("wire = %s, want %s", got, tc.wire)
			}
		})
	}
}

func TestIgnoreOfficialLimitsCannotBypassEncoding(t *testing.T) {
	for _, tc := range []struct{ kind, input string }{
		{"hex", "reporting: {high_frequency_interval: 65536s}"},
		{"hex", "reporting: {upload_interval: 65536m}"},
		{"hex", "reporting: {upload_interval: -1m}"},
		{"hex", "reporting: {upload_interval: 90s}"},
		{"hex", "display: {clock_format: 256}"},
		{"hex", "display: {temperature_unit: -1}"},
		{"hex", "display: {led_enabled: 256}"},
		{"hex", "clock: {sntp_enabled: 'true'}"},
		{"hex", "clock: {timezone: 32768}"},
		{"hex", "clock: {timezone: '+3276:48'}"},
		{"hex", "clock: {timezone: '+05:45'}"},
		{"hex", "clock: {sntp_server: 123}"},
		{"hex", "calibration: {temperature: {offset_c: 0.01}}"},
		{"hex", "calibration: {temperature: {offset_c: -3276.9}}"},
		{"hex", "co2: {indicator: {yellow_min_ppm: 65536, yellow_max_ppm: 1000}}"},
		{"json", "reporting: {record_interval: 2147483648s}"},
		{"json", "reporting: {record_interval: -2147483649s}"},
		{"json", "display: {off_after: 1.5s}"},
		{"json", "display: {temperature_unit: 3}"},
		{"json", "display: {clock_format: 2147483648}"},
		{"json", "display: {pages: [true]}"},
		{"json", "display: {night_mode: {end: 2147483648}}"},
		{"json", "display: {night_mode: {end: '35791394:08'}}"},
		{"json", "display: {night_mode: {end: '24:60'}}"},
		{"json", "clock: {timezone: -2147483649}"},
		{"json", "calibration: {co2: {offset_ppm: 2147483648}}"},
		{"json", "calibration: {temperature: {offset_c: 0.001}}"},
		{"json", "indicators: {humidity_thresholds_percent_rh: [0.001]}"},
		{"json", "indicators: {co2_thresholds_ppm: [-2147483649]}"},
		{"hex", "threshold_alarms: [{sensor: co2, condition: above, threshold: 65536, unit: ppm, repeat: once, start: '00:00', end: '00:00'}]"},
		{"hex", "threshold_alarms: [{sensor: co2, condition: above, threshold: 1000, unit: ppm, repeat: 256, start: 0, end: 0}]"},
		{"hex", "threshold_alarms: [{sensor: co2, condition: above, threshold: 1000, unit: ppm, repeat: 1, start: 4294967296, end: 0}]"},
		{"hex", "threshold_alarms: [{sensor: co2, condition: above, threshold: 1000, unit: ppm, repeat: 1, start: -1, end: 0}]"},
		{"hex", "threshold_alarms: [{sensor: co2, condition: above, threshold: 1000, unit: ppm, repeat: daily, start: '00:00', end: '00:00', sound_duration: 65536s}]"},
		{"hex", "display: {unknown: 1}"},
		{"json", "clock: {sntp_enabled: true}"},
		{"json", "display: {clock_format: 3}\nraw: {json: {is_12_hour_mode: 4}}"},
	} {
		t.Run(tc.kind+"/"+tc.input, func(t *testing.T) {
			if _, err := Build(tc.kind, values(t, tc.input), Options{IgnoreOfficialLimits: true}); err == nil {
				t.Fatal("unrepresentable or structurally invalid input accepted")
			}
		})
	}
}

func TestFirmwareExceptionsNeedNoFlag(t *testing.T) {
	v := values(t, "reporting: {record_interval: 10s}\ndisplay: {rotate_interval: 120s}")
	for _, opt := range []Options{{}, {IgnoreOfficialLimits: true}} {
		if _, err := Build("json", v, opt); err != nil {
			t.Fatal(err)
		}
	}
}
