package p2p

import (
	"os"

	"github.com/cometbft/cometbft/p2p/conn"
)

// benchVarFramesEnv forces variable SecretConnection frames off ("0") or on
// ("1", the default) for benchmarks.
const benchVarFramesEnv = "CMT_BENCH_VARFRAMES"

// DefaultSecretConnFrameVersion is the frame version a node advertises.
func DefaultSecretConnFrameVersion() uint32 {
	if os.Getenv(benchVarFramesEnv) == "0" {
		return conn.FrameVersionFixed
	}
	return conn.FrameVersionVariable
}

// negotiatedFrameVersion is the highest frame version both peers speak.
func negotiatedFrameVersion(ours, theirs NodeInfo) uint32 {
	o, ok := ours.(DefaultNodeInfo)
	if !ok {
		return conn.FrameVersionFixed
	}
	t, ok := theirs.(DefaultNodeInfo)
	if !ok {
		return conn.FrameVersionFixed
	}
	return min(o.SecretConnFrameVersion, t.SecretConnFrameVersion)
}
