package protocol

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func TestFramingAndUnknownFields(t *testing.T) {
	// Independent arithmetic: CG, cmd 32, payload length 5, TLV 04/2/5 => checksum cc.
	raw, _ := hex.DecodeString("43473205000402000500cc00")
	m, err := Decode(raw, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 1 || m.Entries[0].Key != 4 || m.Entries[0].Offset != 5 {
		t.Fatalf("%+v", m)
	}
	encoded, err := Encode(m)
	if err != nil || !bytes.Equal(raw, encoded) {
		t.Fatalf("roundtrip %x %v", encoded, err)
	}
	entries := []TLV{{Key: 0xfe, Value: []byte{1}}, {Key: 0xfe, Value: []byte{2}}}
	raw, err = EncodeHEX(0xa0, entries)
	if err != nil {
		t.Fatal(err)
	}
	m, err = Decode(raw, "hex")
	if err != nil || len(m.Entries) != 2 {
		t.Fatalf("unknown/repeated fields: %v", err)
	}
}

func TestMalformed(t *testing.T) {
	valid, _ := EncodeHEX(0x31, []TLV{{Key: 3, Value: []byte{1, 2}}})
	for i := 0; i < len(valid); i++ {
		if _, err := Decode(valid[:i], "hex"); err == nil {
			t.Errorf("accepted truncation %d", i)
		}
	}
	bad := bytes.Clone(valid)
	bad[len(bad)-1] ^= 1
	if _, err := Decode(bad, "hex"); err == nil {
		t.Fatal("accepted bad checksum")
	}
	// Valid outer frame, broken inner TLV.
	bad = []byte{'C', 'G', 0x31, 3, 0, 4, 2, 0}
	sum := 0
	for _, b := range bad {
		sum += int(b)
	}
	bad = append(bad, byte(sum), byte(sum>>8))
	if _, err := Decode(bad, "hex"); err == nil {
		t.Fatal("accepted overlong TLV")
	}
	for _, s := range []string{`{"type":"17","type":"18"}`, `{"a":{"x":1,"x":2}}`, `{} {}`, `[]`, `null`, `{"a":`} {
		if _, err := Decode([]byte(s), "json"); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	if _, err := Decode([]byte(" \n {\"type\":\"17\",\"unknown\":{\"x\":12345678901234567}}\n"), "auto"); err != nil {
		t.Fatal(err)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte("{}"))
	raw, _ := EncodeHEX(0x32, []TLV{{Key: 4, Value: []byte{5, 0}}})
	f.Add(raw)
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, kind := range []string{"auto", "hex", "json"} {
			m, err := Decode(b, kind)
			if err != nil {
				continue
			}
			encoded, err := Encode(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Decode(encoded, kind); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestJSONNestingBound(t *testing.T) {
	raw := []byte(`{"type":"13","data":` + strings.Repeat("[", 256) + "0" + strings.Repeat("]", 256) + "}")
	if _, err := Decode(raw, "json"); err == nil {
		t.Fatal("deep nesting accepted")
	}
}
