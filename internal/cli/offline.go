package cli

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/command"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/protocol"
	"github.com/spf13/cobra"
)

func decodeCommand() *cobra.Command {
	var kind, model, direction, representation, file, at string
	var explain bool
	cmd := &cobra.Command{Use: "decode [PAYLOAD]", Short: "Decode raw, hex-text or base64 payloads without MQTT", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		b, err := input(cmd, file, args, protocol.MaxPayload*3)
		if err != nil {
			return err
		}
		switch representation {
		case "raw":
		case "hex":
			b, err = hex.DecodeString(strings.Join(strings.Fields(string(b)), ""))
		case "base64":
			b, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		default:
			return errors.New("input-format must be raw, hex or base64")
		}
		if err != nil {
			return errors.New("invalid input representation")
		}
		if explain {
			m, err := protocol.Decode(b, kind)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), protocol.Explain(m))
		}
		received := time.Now().UTC()
		if at != "" {
			received, err = time.Parse(time.RFC3339Nano, at)
			if err != nil {
				return errors.New("--at requires RFC3339")
			}
		}
		e, err := device.Decode(b, kind, model, domain.Direction(direction), received)
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), e)
	}}
	cmd.Flags().StringVar(&kind, "protocol", "auto", "auto, hex or json (wire protocol)")
	cmd.Flags().StringVar(&model, "model", "", "Model profile, e.g. CGP22C or CGDN1")
	cmd.Flags().StringVar(&direction, "direction", string(domain.FromDevice), "from-device or to-device")
	cmd.Flags().StringVar(&representation, "input-format", "raw", "raw, hex or base64 (input representation)")
	cmd.Flags().StringVar(&file, "file", "-", "Input file, or - for stdin")
	cmd.Flags().StringVar(&at, "at", "", "Receive time in RFC3339; defaults to now")
	cmd.Flags().BoolVar(&explain, "explain", false, "Show wire fields, including sensitive raw data")
	return cmd
}
func (a *app) encodeCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "encode", Short: "Construct payloads offline; nothing is sent"}
	settings := a.encodeSettingsCommand()
	var kind, hexCommand, file, rawFormat string
	var fields []string
	var newRequestID bool
	raw := &cobra.Command{Use: "raw", Short: "Encode a JSON envelope or ordered HEX TLV fields", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var b []byte
		var err error
		switch kind {
		case "json":
			if len(fields) > 0 || cmd.Flags().Changed("command") {
				return errors.New("HEX flags cannot be used with JSON")
			}
			b, err = input(cmd, file, nil, protocol.MaxPayload)
			if err != nil {
				return err
			}
			m, e := protocol.Decode(b, "json")
			if e != nil {
				return e
			}
			if newRequestID {
				id, e := command.NewRequestID()
				if e != nil {
					return e
				}
				m.Fields["id"] = json.RawMessage(strconv.FormatUint(uint64(id), 10))
			}
			b, err = protocol.Encode(m)
		case "hex":
			if newRequestID {
				return errors.New("--new-request-id applies only to JSON")
			}
			if file != "-" {
				return errors.New("--file applies to JSON; use --tlv for HEX")
			}
			n, e := strconv.ParseUint(hexCommand, 0, 8)
			if e != nil {
				return errors.New("command must be a byte, e.g. 0x32")
			}
			entries := []protocol.TLV{}
			for _, field := range fields {
				k, v, ok := strings.Cut(field, "=")
				key, e := strconv.ParseUint(k, 0, 8)
				if !ok || e != nil {
					return errors.New("TLV syntax is 0xKEY=HEXVALUE")
				}
				value, e := hex.DecodeString(v)
				if e != nil {
					return errors.New("invalid TLV hex value")
				}
				entries = append(entries, protocol.TLV{Key: byte(key), Value: value})
			}
			b, err = protocol.EncodeHEX(byte(n), entries)
		default:
			return errors.New("protocol must be hex or json")
		}
		if err != nil {
			return err
		}
		return outputPayload(cmd, b, rawFormat)
	}}
	raw.Flags().BoolVar(&newRequestID, "new-request-id", false, "Generate a random 31-bit JSON request ID, replacing any existing id")
	raw.Flags().StringVar(&kind, "protocol", "hex", "hex or json")
	raw.Flags().StringVar(&hexCommand, "command", "0x32", "HEX message command byte")
	raw.Flags().StringArrayVar(&fields, "tlv", nil, "Ordered field 0xKEY=HEXVALUE; repeat to retain duplicate keys")
	raw.Flags().StringVar(&file, "file", "-", "JSON envelope input file, or - for stdin")
	raw.Flags().StringVar(&rawFormat, "output-format", "hex", "hex, base64, raw or json")
	cmd.AddCommand(settings, raw)
	return cmd
}
func outputPayload(cmd *cobra.Command, b []byte, format string) error {
	switch format {
	case "hex":
		_, err := fmt.Fprintln(cmd.OutOrStdout(), hex.EncodeToString(b))
		return err
	case "base64":
		_, err := fmt.Fprintln(cmd.OutOrStdout(), base64.StdEncoding.EncodeToString(b))
		return err
	case "raw":
		_, err := cmd.OutOrStdout().Write(b)
		return err
	case "json":
		m, err := protocol.Decode(b, "auto")
		if err != nil {
			return err
		}
		if m.Protocol == "json" {
			return writeJSON(cmd.OutOrStdout(), m.Fields)
		}
		return writeJSON(cmd.OutOrStdout(), protocol.Explain(m))
	default:
		return errors.New("output-format must be hex, base64, raw or json")
	}
}
