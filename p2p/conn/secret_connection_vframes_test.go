package conn

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	cmtrand "github.com/cometbft/cometbft/libs/rand"
)

// captureWrite returns the wire bytes sc.Write(data) produces without
// sending them.
func captureWrite(t *testing.T, sc *SecretConnection, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	sc.connWriter.Reset(&buf)
	defer sc.connWriter.Reset(sc.conn)
	n, err := sc.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)
	return buf.Bytes()
}

// sealVariable builds one variable frame by hand: hdr on the wire, aad
// as the authenticated length (normally equal to hdr).
func sealVariable(sc *SecretConnection, payload []byte, hdr, aad uint16) []byte {
	var h, a [varFrameLenSize]byte
	binary.BigEndian.PutUint16(h[:], hdr)
	binary.BigEndian.PutUint16(a[:], aad)
	out := sc.sendAead.Seal(h[:], sc.sendNonce[:], payload, a[:])
	incrNonce(sc.sendNonce)
	return out
}

// feed writes raw bytes to the peer of sc and then closes the stream.
func feed(sc *SecretConnection, raw []byte) {
	w := sc.conn.(kvstoreConn).PipeWriter
	go func() {
		_, _ = w.Write(raw)
		_ = w.Close()
	}()
}

func variablePair(t *testing.T) (foo, bar *SecretConnection) {
	t.Helper()
	foo, bar = makeSecretConnPair(t)
	require.NoError(t, foo.EnableVariableFrames())
	require.NoError(t, bar.EnableVariableFrames())
	require.True(t, foo.VariableFrames())
	t.Cleanup(func() { _ = foo.Close(); _ = bar.Close() })
	return foo, bar
}

func TestSecretConnectionFrameRoundTrip(t *testing.T) {
	sizes := []int{1, 100, dataMaxSize - 1, dataMaxSize, dataMaxSize + 1, 3*dataMaxSize + 7}
	for _, variable := range []bool{false, true} {
		foo, bar := makeSecretConnPair(t)
		if variable {
			require.NoError(t, foo.EnableVariableFrames())
			require.NoError(t, bar.EnableVariableFrames())
		}
		for _, size := range sizes {
			msg := cmtrand.Bytes(size)
			wire := captureWrite(t, foo, msg)
			frames := (size + dataMaxSize - 1) / dataMaxSize
			want := frames * (totalFrameSize + aeadSizeOverhead)
			if variable {
				want = size + frames*(varFrameLenSize+aeadSizeOverhead)
			}
			require.Equal(t, want, len(wire), "variable=%v size=%d", variable, size)

			go func() { _, _ = foo.conn.Write(wire) }()
			got := make([]byte, size)
			_, err := io.ReadFull(bar, got)
			require.NoError(t, err)
			require.Equal(t, msg, got)
		}
		_ = foo.Close()
		_ = bar.Close()
	}
}

// Partial reads keep the rest of a frame for the next Read in both modes.
func TestSecretConnectionVariablePartialRead(t *testing.T) {
	foo, bar := variablePair(t)
	msg := cmtrand.Bytes(700)
	go func() { _, _ = foo.Write(msg) }()
	var got []byte
	buf := make([]byte, 64)
	for len(got) < len(msg) {
		n, err := bar.Read(buf)
		require.NoError(t, err)
		got = append(got, buf[:n]...)
	}
	require.Equal(t, msg, got)
}

// The nonce counter carries across the switch in both directions.
func TestSecretConnectionSwitchNonceContinuity(t *testing.T) {
	foo, bar := makeSecretConnPair(t)
	t.Cleanup(func() { _ = foo.Close(); _ = bar.Close() })

	exchange := func(from, to *SecretConnection, msg []byte) {
		go func() { _, _ = from.Write(msg) }()
		got := make([]byte, len(msg))
		_, err := io.ReadFull(to, got)
		require.NoError(t, err)
		require.Equal(t, msg, got)
	}
	exchange(foo, bar, cmtrand.Bytes(2500)) // 3 fixed frames
	exchange(bar, foo, cmtrand.Bytes(10))
	before := *foo.sendNonce
	require.Equal(t, *foo.sendNonce, *bar.recvNonce)
	require.Equal(t, *bar.sendNonce, *foo.recvNonce)

	require.NoError(t, foo.EnableVariableFrames())
	require.NoError(t, bar.EnableVariableFrames())
	exchange(foo, bar, cmtrand.Bytes(2500))
	exchange(bar, foo, cmtrand.Bytes(10))
	require.Equal(t, *foo.sendNonce, *bar.recvNonce)
	require.Equal(t, *bar.sendNonce, *foo.recvNonce)
	require.Equal(t, binary.LittleEndian.Uint64(before[4:])+3, binary.LittleEndian.Uint64(foo.sendNonce[4:]))
}

func TestSecretConnectionEnableVariableMidFrame(t *testing.T) {
	foo, bar := makeSecretConnPair(t)
	t.Cleanup(func() { _ = foo.Close(); _ = bar.Close() })
	go func() { _, _ = foo.Write([]byte("hello")) }()
	buf := make([]byte, 2)
	_, err := io.ReadFull(bar, buf)
	require.NoError(t, err)
	require.Error(t, bar.EnableVariableFrames())
	require.False(t, bar.VariableFrames())
}

func TestSecretConnectionVariableReadRejects(t *testing.T) {
	payload := []byte("hello")
	cases := []struct {
		name  string
		raw   func(foo *SecretConnection) []byte
		check func(t *testing.T, err error)
	}{
		{"length zero", func(*SecretConnection) []byte { return []byte{0, 0, 1, 2, 3} },
			func(t *testing.T, err error) { require.ErrorIs(t, err, ErrInvalidFrameLength) }},
		{"length max+1", func(*SecretConnection) []byte { return []byte{0x04, 0x01} },
			func(t *testing.T, err error) { require.ErrorIs(t, err, ErrInvalidFrameLength) }},
		{"length 0xffff", func(*SecretConnection) []byte { return []byte{0xff, 0xff} },
			func(t *testing.T, err error) { require.ErrorIs(t, err, ErrInvalidFrameLength) }},
		{"truncated header", func(*SecretConnection) []byte { return []byte{0} },
			func(t *testing.T, err error) { require.ErrorIs(t, err, io.ErrUnexpectedEOF) }},
		{"header only", func(*SecretConnection) []byte { return []byte{0, 5} },
			func(t *testing.T, err error) { require.ErrorIs(t, err, io.ErrUnexpectedEOF) }},
		{"truncated body", func(foo *SecretConnection) []byte {
			f := sealVariable(foo, payload, 5, 5)
			return f[:len(f)-1]
		}, func(t *testing.T, err error) { require.ErrorIs(t, err, io.ErrUnexpectedEOF) }},
		{"length not authenticated as sent", func(foo *SecretConnection) []byte {
			return sealVariable(foo, payload, 5, 6) // aad differs from the wire header
		}, func(t *testing.T, err error) { require.ErrorContains(t, err, "decrypt") }},
		{"tampered header", func(foo *SecretConnection) []byte {
			f := sealVariable(foo, payload, 5, 5)
			f[1] = 4
			return f
		}, func(t *testing.T, err error) { require.ErrorContains(t, err, "decrypt") }},
		{"tampered tag", func(foo *SecretConnection) []byte {
			f := sealVariable(foo, payload, 5, 5)
			f[len(f)-1] ^= 1
			return f
		}, func(t *testing.T, err error) { require.ErrorContains(t, err, "decrypt") }},
		{"legacy frame", func(foo *SecretConnection) []byte {
			foo.sendVariable = false
			return captureWrite(t, foo, payload)
		}, func(t *testing.T, err error) {
			require.Error(t, err)
			require.False(t, errors.Is(err, io.EOF))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			foo, bar := variablePair(t)
			feed(foo, tc.raw(foo))
			n, err := bar.Read(make([]byte, dataMaxSize))
			require.Zero(t, n)
			tc.check(t, err)
		})
	}
}

// A fixed-frame reader rejects variable frames.
func TestSecretConnectionFixedReaderRejectsVariable(t *testing.T) {
	foo, bar := variablePair(t)
	bar.recvVariable = false
	wire := captureWrite(t, foo, cmtrand.Bytes(dataMaxSize+500))
	feed(foo, wire)
	_, err := bar.Read(make([]byte, dataMaxSize))
	require.Error(t, err)
	require.False(t, errors.Is(err, io.EOF))
}
