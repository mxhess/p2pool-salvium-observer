package levin_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client/levin"
)

func TestPortableStorage(t *testing.T) {
	t.Parallel()
	t.Run("NewPortableStorageFromBytes", func(t *testing.T) {
		it(t, "fails w/ wrong sigA", func(t *testing.T) {
			bytes := []byte{
				0xaa, 0xaa, 0xaa, 0xaa,
			}

			_, err := levin.NewPortableStorageFromBytes(bytes)
			assertError(t, err)
			assertContains(t, err.Error(), "sig-a doesn't match")
		})

		it(t, "fails w/ wrong sigB", func(t *testing.T) {
			bytes := []byte{
				0x01, 0x11, 0x01, 0x01,
				0xaa, 0xaa, 0xaa, 0xaa,
			}

			_, err := levin.NewPortableStorageFromBytes(bytes)
			assertError(t, err)
			assertContains(t, err.Error(), "sig-b doesn't match")
		})

		it(t, "fails w/ wrong format ver", func(t *testing.T) {
			bytes := []byte{
				0x01, 0x11, 0x01, 0x01,
				0x01, 0x01, 0x02, 0x01,
				0xaa,
			}

			_, err := levin.NewPortableStorageFromBytes(bytes)
			assertError(t, err)
			assertContains(t, err.Error(), "version doesn't match")
		})

		it(t, "reads the contents", func(t *testing.T) {
			bytes := []byte{
				0x01, 0x11, 0x01, 0x01, // sig a
				0x01, 0x01, 0x02, 0x01, // sig b
				0x01, // format ver

				0x08, // var_in(len(entries))

				// node_data
				0x09,                                                 // len("node_data")
				0x6e, 0x6f, 0x64, 0x65, 0x5f, 0x64, 0x61, 0x74, 0x61, // "node_data"
				0x0c, // boost_serialized_obj
				0x04, // var_in(node_data.entries)

				// for i in range node_data
				0x03,             // len("foo")
				0x66, 0x6f, 0x6f, // "foo"
				0x0a,             // boost_serialized_string
				0xc,              // var_in(len("bar"))
				0x62, 0x61, 0x72, // "bar"

				// payload_data
				0x0c,                                                                   // len("payload_data")
				0x70, 0x61, 0x79, 0x6c, 0x6f, 0x61, 0x64, 0x5f, 0x64, 0x61, 0x74, 0x61, // "payload_data"
				0x0c, // boost_serialized_obj
				0x04, // var_in(payload_data.entries)

				// for i in range payload_data.entries
				0x06,                               // len("number")
				0x6e, 0x75, 0x6d, 0x62, 0x65, 0x72, // "number"
				0x06,                   // boost_serialized_uint32
				0x01, 0x00, 0x00, 0x00, // uint32(1)
			}

			ps, err := levin.NewPortableStorageFromBytes(bytes)
			assertNoError(t, err)

			assertEqual(t, len(ps.Entries), 2, "len")
			assertEqual(t, ps.Entries[0].Name, "node_data")
			assertEqual(t, ps.Entries[0].Value, levin.Entries{
				{
					Name:         "foo",
					Value:        "bar",
					Serializable: levin.BoostString("bar"),
				},
			})

			assertEqual(t, ps.Entries[1].Name, "payload_data")
			assertEqual(t, ps.Entries[1].Value, levin.Entries{
				{
					Name:         "number",
					Value:        uint32(1),
					Serializable: levin.BoostUint32(1),
				},
			})
		})
	})

	t.Run("ReadVarIn", func(t *testing.T) {
		it(t, "i <= 63", func(t *testing.T) {
			b := []byte{0x08}
			n, v, _ := levin.ReadVarInt(b)

			assertEqual(t, n, 1)
			assertEqual(t, v, 2)
		})

		it(t, "64 <= i <= 16383", func(t *testing.T) {
			b := []byte{0x01, 0x02}
			n, v, _ := levin.ReadVarInt(b)
			assertEqual(t, n, 2)
			assertEqual(t, v, 128)
		})

		it(t, "16384 <= i <= 1073741823", func(t *testing.T) {
			b := []byte{0x02, 0x00, 0x01, 0x00}
			n, v, _ := levin.ReadVarInt(b)
			assertEqual(t, n, 4)
			assertEqual(t, v, 16384)
		})
	})

	t.Run("VarrIn", func(t *testing.T) {
		it(t, "i <= 63", func(t *testing.T) {
			i := 2 // 0b00000010

			b, err := levin.VarIn(i)
			assertNoError(t, err)
			assertEqual(t, b, []byte{
				0x08, // 0b00001000	(shift left twice, union 0)
			})
		})

		it(t, "64 <= i <= 16383", func(t *testing.T) {
			i := 128 // 0b010000000

			b, err := levin.VarIn(i)
			assertNoError(t, err)
			assertEqual(t, b, []byte{
				0x01, 0x02, // 0b1000000001 ((128 * 2 * 2) | 1) == 513
				// '    '
				// 1   2 * 256
			})
		})

		it(t, "16384 <= i <= 1073741823", func(t *testing.T) {
			i := 16384 // 1 << 14

			b, err := levin.VarIn(i)
			assertNoError(t, err)
			assertEqual(t, b, []byte{
				0x02, 0x00, 0x01, 0x00, // (1 << 16) | 2
			})
		})
	})

	t.Run("PortableStorage", func(t *testing.T) {
		it(t, "bytes", func(t *testing.T) {
			ps := &levin.PortableStorage{
				Entries: []levin.Entry{
					{
						Name: "node_data",
						Serializable: &levin.Section{
							Entries: []levin.Entry{
								{
									Name:         "foo",
									Serializable: levin.BoostString("bar"),
								},
							},
						},
					},
					{
						Name: "payload_data",
						Serializable: &levin.Section{
							Entries: []levin.Entry{
								{
									Name:         "number",
									Serializable: levin.BoostUint32(1),
								},
							},
						},
					},
				},
			}

			data, _ := ps.Bytes()

			assertEqual(t, []byte{
				0x01, 0x11, 0x01, 0x01, // sig a
				0x01, 0x01, 0x02, 0x01, // sig b
				0x01, // format ver
				0x08, // var_in(len(entries))

				// node_data
				0x09,                                                 // len("node_data")
				0x6e, 0x6f, 0x64, 0x65, 0x5f, 0x64, 0x61, 0x74, 0x61, // "node_data"
				0x0c, // boost_serialized_obj
				0x04, // var_in(node_data.entries)

				// for i in range node_data
				0x03,             // len("foo")
				0x66, 0x6f, 0x6f, // "foo"
				0x0a,             // boost_serialized_string
				0xc,              // var_in(len("bar"))
				0x62, 0x61, 0x72, // "bar"

				// payload_data
				0x0c,                                                                   // len("payload_data")
				0x70, 0x61, 0x79, 0x6c, 0x6f, 0x61, 0x64, 0x5f, 0x64, 0x61, 0x74, 0x61, // "payload_data"
				0x0c, // boost_serialized_obj
				0x04, // var_in(payload_data.entries)

				// for i in range payload_data.entries
				0x06,                               // len("number")
				0x6e, 0x75, 0x6d, 0x62, 0x65, 0x72, // "number"
				0x06,                   // boost_serialized_uint32
				0x01, 0x00, 0x00, 0x00, // uint32(1)

			}, data)
		})
	})
}

func FuzzLevinPortableStorage_RoundTrip(f *testing.F) {
	f.Fuzz(func(t *testing.T, buf []byte) {
		// enforce capacity checks
		buf = buf[:len(buf):len(buf)]

		ps, err := levin.NewPortableStorageFromBytes(buf)
		if err != nil {
			t.Skip(err)
		}
		data, err := ps.Bytes()
		if err != nil {
			t.Skip(err)
		}
		if !bytes.Equal(buf, data) {
			t.Logf("EXPECTED (len %d):\n%s", len(buf), hex.Dump(buf))
			t.Logf("ACTUAL (len %d):\n%s", len(data), hex.Dump(data))
			t.Fatalf("mismatched roundtrip")
		}
	})
}
