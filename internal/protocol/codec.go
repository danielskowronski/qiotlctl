// Package protocol implements lossless wire framing without model or I/O state.
package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const MaxPayload = 4 << 20

type TLV struct {
	Key    byte   `json:"key"`
	Offset int    `json:"offset"`
	Value  []byte `json:"value"`
}

type Message struct {
	Protocol string
	Command  byte
	Entries  []TLV
	Fields   map[string]json.RawMessage
	Raw      []byte
}

func Decode(raw []byte, kind string) (Message, error) {
	if len(raw) > MaxPayload {
		return Message{}, errors.New("payload exceeds 4 MiB limit")
	}
	if kind == "" || kind == "auto" {
		switch {
		case bytes.HasPrefix(raw, []byte("CG")):
			kind = "hex"
		case bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")):
			kind = "json"
		default:
			return Message{}, errors.New("unrecognized protocol; specify the input representation separately")
		}
	}
	switch kind {
	case "hex":
		return decodeHEX(raw)
	case "json":
		// Reject duplicate object keys, including nested ones. Silent last-key-wins
		// behavior would make offline inspection and future command matching unsafe.
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := checkJSON(d, 0); err != nil {
			return Message{}, fmt.Errorf("invalid JSON: %w", err)
		}
		if _, err := d.Token(); err != io.EOF {
			return Message{}, errors.New("trailing JSON content")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return Message{}, errors.New("JSON envelope must be an object")
		}
		return Message{Protocol: kind, Fields: fields, Raw: bytes.Clone(raw)}, nil
	default:
		return Message{}, fmt.Errorf("unsupported protocol %q", kind)
	}
}

func checkJSON(d *json.Decoder, depth int) error {
	if depth > 128 {
		return errors.New("JSON nesting exceeds 128 levels")
	}
	t, err := d.Token()
	if err != nil {
		return errors.New("malformed value")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return errors.New("malformed object")
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return errors.New("duplicate or invalid object key")
			}
			seen[key] = true
			if err = checkJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err = checkJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected delimiter")
	}
	_, err = d.Token()
	return err
}

func decodeHEX(raw []byte) (Message, error) {
	if len(raw) < 7 || !bytes.HasPrefix(raw, []byte("CG")) {
		return Message{}, errors.New("HEX frame requires CG header and at least 7 bytes")
	}
	n := int(binary.LittleEndian.Uint16(raw[3:5]))
	if n+7 != len(raw) {
		return Message{}, fmt.Errorf("HEX frame length: header=%d actual=%d", n+7, len(raw))
	}
	var sum uint16
	for _, b := range raw[:len(raw)-2] {
		sum += uint16(b)
	}
	if sum != binary.LittleEndian.Uint16(raw[len(raw)-2:]) {
		return Message{}, errors.New("HEX checksum mismatch")
	}
	m := Message{Protocol: "hex", Command: raw[2], Raw: bytes.Clone(raw)}
	for pos := 5; pos < 5+n; {
		if 5+n-pos < 3 {
			return Message{}, fmt.Errorf("truncated TLV header at byte %d", pos)
		}
		size := int(binary.LittleEndian.Uint16(raw[pos+1 : pos+3]))
		if size > 5+n-pos-3 {
			return Message{}, fmt.Errorf("truncated TLV value at byte %d", pos)
		}
		m.Entries = append(m.Entries, TLV{Key: raw[pos], Offset: pos, Value: bytes.Clone(raw[pos+3 : pos+3+size])})
		pos += 3 + size
	}
	return m, nil
}

func EncodeHEX(command byte, entries []TLV) ([]byte, error) {
	n := 0
	for _, e := range entries {
		n += 3 + len(e.Value)
		if len(e.Value) > 65535 || n > 65535 {
			return nil, errors.New("HEX payload exceeds uint16 length")
		}
	}
	out := make([]byte, 5, 7+n)
	copy(out, "CG")
	out[2] = command
	binary.LittleEndian.PutUint16(out[3:5], uint16(n))
	for _, e := range entries {
		out = append(out, e.Key, byte(len(e.Value)), byte(len(e.Value)>>8))
		out = append(out, e.Value...)
	}
	var sum uint16
	for _, b := range out {
		sum += uint16(b)
	}
	out = append(out, byte(sum), byte(sum>>8))
	return out, nil
}

// Encode preserves unknown/repeated HEX fields and every JSON field. JSON
// whitespace is canonicalized; Raw can be used when byte identity is required.
func Encode(m Message) ([]byte, error) {
	if m.Protocol == "hex" {
		return EncodeHEX(m.Command, m.Entries)
	}
	if m.Protocol != "json" || m.Fields == nil {
		return nil, errors.New("unsupported wire message")
	}
	b, err := json.Marshal(m.Fields)
	if err != nil {
		return nil, err
	}
	_, err = Decode(b, "json")
	return b, err
}

func Explain(m Message) any {
	if m.Protocol == "json" {
		return m.Fields
	}
	type entry struct {
		Key    string `json:"key"`
		Offset int    `json:"offset"`
		Length int    `json:"length"`
		Hex    string `json:"hex"`
	}
	entries := []entry{}
	for _, e := range m.Entries {
		entries = append(entries, entry{fmt.Sprintf("0x%02x", e.Key), e.Offset, len(e.Value), hex.EncodeToString(e.Value)})
	}
	return struct {
		Command  string  `json:"command"`
		Checksum string  `json:"checksum"`
		Entries  []entry `json:"entries"`
	}{fmt.Sprintf("0x%02x", m.Command), "valid", entries}
}
