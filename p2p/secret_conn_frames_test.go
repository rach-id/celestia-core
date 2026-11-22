package p2p

import (
	"io"
	"net"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	"github.com/cometbft/cometbft/p2p/conn"
)

func TestDefaultSecretConnFrameVersion(t *testing.T) {
	for env, want := range map[string]uint32{"": 1, "1": 1, "0": 0} {
		t.Setenv(benchVarFramesEnv, env)
		require.Equal(t, want, DefaultSecretConnFrameVersion(), "env=%q", env)
	}
}

func TestNodeInfoFrameVersionProto(t *testing.T) {
	ni := testNodeInfo(PubKeyToID(ed25519.GenPrivKey().PubKey()), "a").(DefaultNodeInfo)
	ni.SecretConnFrameVersion = 1
	got, err := DefaultNodeInfoFromToProto(ni.ToProto())
	require.NoError(t, err)
	require.Equal(t, ni, got)
}

// countingConn counts bytes written to the socket.
type countingConn struct {
	net.Conn
	written atomic.Int64
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.written.Add(int64(n))
	return n, err
}

func TestTransportFrameVersionNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name           string
		dialer, accept uint32
		variable       bool
	}{
		{"both", 1, 1, true},
		{"dialer only", 1, 0, false},
		{"acceptor only", 0, 1, false},
		{"none", 0, 0, false},
		{"future peer", 1, 7, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newMT := func(version uint32) *MultiplexTransport {
				pv := ed25519.GenPrivKey()
				ni := testNodeInfo(PubKeyToID(pv.PubKey()), "node").(DefaultNodeInfo)
				ni.SecretConnFrameVersion = version
				return newMultiplexTransport(ni, NodeKey{PrivKey: pv})
			}
			dialMT, acceptMT := newMT(tc.dialer), newMT(tc.accept)

			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer ln.Close()

			type result struct {
				sc  *conn.SecretConnection
				raw *countingConn
				err error
			}
			acceptc := make(chan result, 1)
			go func() {
				c, err := ln.Accept()
				if err != nil {
					acceptc <- result{err: err}
					return
				}
				cc := &countingConn{Conn: c}
				sc, _, err := acceptMT.upgrade(cc, nil)
				acceptc <- result{sc, cc, err}
			}()

			c, err := net.Dial("tcp", ln.Addr().String())
			require.NoError(t, err)
			dc := &countingConn{Conn: c}
			addr := NewNetAddress(acceptMT.nodeInfo.ID(), ln.Addr())
			dsc, _, err := dialMT.upgrade(dc, addr)
			require.NoError(t, err)
			ar := <-acceptc
			require.NoError(t, ar.err)
			asc := ar.sc
			defer dsc.Close()
			defer asc.Close()

			require.Equal(t, tc.variable, dsc.VariableFrames())
			require.Equal(t, tc.variable, asc.VariableFrames())

			// Traffic flows both ways after the switch, and the frame size on
			// the wire matches the negotiated mode.
			check := func(from, to *conn.SecretConnection, raw *countingConn, size int) {
				msg := cmtrand.Bytes(size)
				before := raw.written.Load()
				werr := make(chan error, 1)
				go func() { _, err := from.Write(msg); werr <- err }()
				got := make([]byte, size)
				_, err := io.ReadFull(to, got)
				require.NoError(t, err)
				require.Equal(t, msg, got)
				require.NoError(t, <-werr)
				wantWire := int64(1044)
				if tc.variable {
					wantWire = int64(size + 18)
				}
				require.Equal(t, wantWire, raw.written.Load()-before)
			}
			check(dsc, asc, dc, 10)
			check(asc, dsc, ar.raw, 10)
			check(dsc, asc, dc, 1000)
			check(asc, dsc, ar.raw, 1)
		})
	}
}
