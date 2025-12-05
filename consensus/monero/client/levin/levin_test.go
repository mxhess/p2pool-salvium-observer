package levin_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client/levin"
)

func assertNoError(t *testing.T, err error, msgAndArgs ...any) {
	if err != nil {
		message := ""
		if len(msgAndArgs) > 0 {
			message = fmt.Sprint(msgAndArgs...) + ": "
		}
		t.Errorf("%sunexpected err: %s", message, err)
	}
}

func assertError(t *testing.T, err error, msgAndArgs ...any) {
	if err == nil {
		message := ""
		if len(msgAndArgs) > 0 {
			message = fmt.Sprint(msgAndArgs...) + ": "
		}
		t.Errorf("%sexpected err", message)
	}
}

func assertContains(t *testing.T, actual, expected string, msgAndArgs ...any) {
	if !strings.Contains(actual, expected) {
		message := ""
		if len(msgAndArgs) > 0 {
			message = fmt.Sprint(msgAndArgs...) + ": "
		}
		t.Errorf("%sactual: %v expected: %v", message, actual, expected)
	}
}

func assertEqual(t *testing.T, actual, expected any, msgAndArgs ...any) {
	if !reflect.DeepEqual(actual, expected) {
		message := ""
		if len(msgAndArgs) > 0 {
			message = fmt.Sprint(msgAndArgs...) + ": "
		}
		t.Errorf("%sactual: %v expected: %v", message, actual, expected)
	}
}

func it(t *testing.T, msg string, f func(t *testing.T)) {
	t.Run(msg, func(t *testing.T) {
		f(t)
	})
}

func TestLevin(t *testing.T) {
	t.Parallel()
	t.Run("NewHeaderFromBytes", func(t *testing.T) {
		it(t, "fails w/ wrong size", func(t *testing.T) {
			bytes := []byte{
				0xff,
			}

			_, err := levin.NewHeaderFromBytesBytes(bytes)
			assertError(t, err)
		})

		it(t, "fails w/ wrong signature", func(t *testing.T) {
			bytes := []byte{
				0xff, 0xff, 0xff, 0xff, // signature
				0xff, 0xff, 0xff, 0xff,
				0x00, 0x00, 0x00, 0x00, // length
				0x00, 0x00, 0x00, 0x00, //
				0x00,                   // expects response
				0x00, 0x00, 0x00, 0x00, // command
				0x00, 0x00, 0x00, 0x00, // return code
				0x00, 0x00, 0x00, 0x00, // flags
				0x00, 0x00, 0x00, 0x00, // version
			}

			_, err := levin.NewHeaderFromBytesBytes(bytes)
			assertError(t, err)
			assertContains(t, err.Error(), "signature mismatch")
		})

		it(t, "fails w/ invalid command", func(t *testing.T) {
			bytes := []byte{
				0x01, 0x21, 0x01, 0x01, // signature
				0x01, 0x01, 0x01, 0x01,
				0x01, 0x00, 0x00, 0x00, // length
				0x00, 0x00, 0x00, 0x00, //
				0x01,                   // expects response
				0xff, 0xff, 0xff, 0xff, // command
				0x00, 0x00, 0x00, 0x00, // return code
				0x00, 0x00, 0x00, 0x00, // flags
				0x00, 0x00, 0x00, 0x00, // version
			}

			_, err := levin.NewHeaderFromBytesBytes(bytes)
			assertError(t, err)
			assertContains(t, err.Error(), "invalid command")
		})

		it(t, "fails w/ invalid return code", func(t *testing.T) {
			bytes := []byte{
				0x01, 0x21, 0x01, 0x01, // signature
				0x01, 0x01, 0x01, 0x01,
				0x01, 0x00, 0x00, 0x00, // length
				0x00, 0x00, 0x00, 0x00, //
				0x01,                   // expects response
				0xe9, 0x03, 0x00, 0x00, // command
				0xaa, 0xaa, 0xaa, 0xaa, // return code
				0x00, 0x00, 0x00, 0x00, // flags
				0x00, 0x00, 0x00, 0x00, // version
			}

			_, err := levin.NewHeaderFromBytesBytes(bytes)
			assertError(t, err)
			assertContains(t, err.Error(), "invalid return code")
		})

		it(t, "fails w/ invalid version", func(t *testing.T) {
			bytes := []byte{
				0x01, 0x21, 0x01, 0x01, // signature
				0x01, 0x01, 0x01, 0x01,
				0x01, 0x00, 0x00, 0x00, // length
				0x00, 0x00, 0x00, 0x00, //
				0x01,                   // expects response
				0xe9, 0x03, 0x00, 0x00, // command
				0x00, 0x00, 0x00, 0x00, // return code
				0x02, 0x00, 0x00, 0x00, // flags
				0x00, 0x00, 0x00, 0x00, // version
			}

			_, err := levin.NewHeaderFromBytesBytes(bytes)
			assertError(t, err)
			assertContains(t, err.Error(), "invalid version")
		})

		it(t, "assembles properly from pong", func(t *testing.T) {
			bytes := []byte{
				0x01, 0x21, 0x01, 0x01, // signature
				0x01, 0x01, 0x01, 0x01,
				0x01, 0x00, 0x00, 0x00, // length
				0x00, 0x00, 0x00, 0x00, //
				0x01,                   // expects response
				0xeb, 0x03, 0x00, 0x00, // command
				0x00, 0x00, 0x00, 0x00, // return code
				0x02, 0x00, 0x00, 0x00, // flags
				0x01, 0x00, 0x00, 0x00, // version
			}

			header, err := levin.NewHeaderFromBytesBytes(bytes)
			assertNoError(t, err)
			assertEqual(t, header.Command, levin.CommandPing)
			assertEqual(t, header.ReturnCode, levin.LevinOk)
			assertEqual(t, header.Flags, levin.LevinPacketReponse)
			assertEqual(t, header.Version, levin.LevinProtocolVersion)
		})
	})

	t.Run("NewRequestHeader", func(t *testing.T) {
		it(t, "assembles properly w/ ping", func(t *testing.T) {
			bytes := levin.NewRequestHeader(levin.CommandPing, 1).Bytes()

			assertEqual(t, bytes, []byte{
				0x01, 0x21, 0x01, 0x01, // signature
				0x01, 0x01, 0x01, 0x01,
				0x01, 0x00, 0x00, 0x00, // length		-- 0 for a ping msg
				0x00, 0x00, 0x00, 0x00,
				0x01,                   // expects response	-- `true` bool
				0xeb, 0x03, 0x00, 0x00, // command		-- 1003 for ping
				0x00, 0x00, 0x00, 0x00, // return code		-- 0 for requests
				0x01, 0x00, 0x00, 0x00, // flags		-- Q(1st lsb) set for req
				0x01, 0x00, 0x00, 0x00, // version
			})
		})

		it(t, "assembles properly w/ handshake", func(t *testing.T) {
			bytes := levin.NewRequestHeader(levin.CommandHandshake, 4).Bytes()

			assertEqual(t, bytes, []byte{
				0x01, 0x21, 0x01, 0x01, // signature
				0x01, 0x01, 0x01, 0x01,
				0x04, 0x00, 0x00, 0x00, // length		-- 0 for a ping msg
				0x00, 0x00, 0x00, 0x00,
				0x01, // expects response	-- `true` bool
				0xe9, 0x03, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, // return code		-- 0 for requests
				0x01, 0x00, 0x00, 0x00, // flags		-- Q(1st lsb) set for req
				0x01, 0x00, 0x00, 0x00, // version
			})
		})
	})
}

func FuzzLevinHeader_RoundTrip(f *testing.F) {
	f.Fuzz(func(t *testing.T, buf []byte) {
		// enforce capacity checks
		buf = buf[:len(buf):len(buf)]

		header, err := levin.NewHeaderFromBytesBytes(buf)
		if err != nil {
			t.Skip(err)
		}
		data := header.Bytes()
		if !bytes.Equal(buf, data) {
			t.Logf("EXPECTED (len %d):\n%s", len(buf), hex.Dump(buf))
			t.Logf("ACTUAL (len %d):\n%s", len(data), hex.Dump(data))
			t.Fatalf("mismatched roundtrip")
		}
	})
}
