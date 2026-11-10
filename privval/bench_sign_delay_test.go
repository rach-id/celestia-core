package privval

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cmtrand "github.com/cometbft/cometbft/libs/rand"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

func TestBenchSignDelay(t *testing.T) {
	orig := benchSignDelayFile
	t.Cleanup(func() { benchSignDelayFile = orig })
	benchSignDelayFile = filepath.Join(t.TempDir(), "sign_delay_ms")

	require.Zero(t, benchSignDelay(), "missing file")
	for content, want := range map[string]time.Duration{
		"abc": 0, "-5": 0, "0": 0, "150\n": 150 * time.Millisecond, "999999": maxBenchSignDelay,
	} {
		require.NoError(t, os.WriteFile(benchSignDelayFile, []byte(content), 0o600))
		require.Equal(t, want, benchSignDelay(), content)
	}

	require.NoError(t, os.WriteFile(benchSignDelayFile, []byte("60"), 0o600))
	pv, _, _ := newTestFilePV(t)
	blockID := types.BlockID{Hash: cmtrand.Bytes(32), PartSetHeader: types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(32)}}
	for _, typ := range []cmtproto.SignedMsgType{cmtproto.PrevoteType, cmtproto.PrecommitType} {
		vote := newVote(pv.Key.Address, 0, 1, 0, typ, blockID, nil)
		start := time.Now()
		require.NoError(t, pv.SignVote("chain", vote.ToProto()))
		require.GreaterOrEqual(t, time.Since(start), 60*time.Millisecond, typ.String())
	}

	start := time.Now()
	benchSleepBeforeVote(&cmtproto.Vote{Type: cmtproto.ProposalType})
	require.Less(t, time.Since(start), 30*time.Millisecond, "proposals are not delayed")
}
