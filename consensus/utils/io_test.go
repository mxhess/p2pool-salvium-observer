package utils

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func TestReadFullProgressive(t *testing.T) {

	// 16 MiB
	dataBuf := make([]byte, 1024*1024*16)
	_, _ = rand.Read(dataBuf)

	{
		var outBuffer []byte

		n, err := ReadFullProgressive(bytes.NewReader(dataBuf), &outBuffer, len(dataBuf))
		if err != nil {
			t.Fatal(err)
		}
		if n != len(dataBuf) {
			t.Fatalf("got n %d, want %d", n, len(dataBuf))
		}
		if !bytes.Equal(outBuffer, dataBuf) {
			t.Fatalf("got buffer %s, want %s", outBuffer, dataBuf)
		}
	}

	{
		var outBuffer []byte

		n, err := ReadFullProgressive(io.LimitReader(bytes.NewReader(dataBuf), int64(len(dataBuf)-1)), &outBuffer, len(dataBuf))
		if err != nil {
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal(err)
			}
		} else {
			t.Fatal("expected ErrUnexpectedEOF")
		}
		if n != len(dataBuf)-1 {
			t.Fatalf("got n %d, want %d", n, len(dataBuf)-1)
		}
	}
}
