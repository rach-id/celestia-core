package consensus

import (
	"testing"

	"github.com/stretchr/testify/require"

	cstypes "github.com/cometbft/cometbft/consensus/types"
	cmtcons "github.com/cometbft/cometbft/proto/tendermint/consensus"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

func TestHasVoteEnvelope(t *testing.T) {
	vote := func(h int64) *types.Vote {
		return &types.Vote{Height: h, Round: 1, Type: cmtproto.PrecommitType, ValidatorIndex: 7}
	}
	want := &cmtcons.HasVote{Height: 5, Round: 1, Type: cmtproto.PrecommitType, Index: 7}

	t.Run("defaults: eager, past heights sent", func(t *testing.T) {
		conR := &Reactor{rs: &cstypes.RoundState{Height: 5}}
		e, ok := conR.hasVoteEnvelope(vote(5))
		require.True(t, ok)
		require.False(t, e.Lazy)
		require.Equal(t, StateChannel, e.ChannelID)
		require.Equal(t, want, e.Message)
		_, ok = conR.hasVoteEnvelope(vote(4))
		require.True(t, ok)
	})

	t.Run("lazy", func(t *testing.T) {
		conR := &Reactor{rs: &cstypes.RoundState{Height: 5}, lazyHasVote: true}
		e, ok := conR.hasVoteEnvelope(vote(5))
		require.True(t, ok)
		require.True(t, e.Lazy)
		require.Equal(t, want, e.Message)
	})

	t.Run("skip stale", func(t *testing.T) {
		conR := &Reactor{rs: &cstypes.RoundState{Height: 5}, skipStaleHasVote: true}
		_, ok := conR.hasVoteEnvelope(vote(4))
		require.False(t, ok, "receivers at height 5 ignore a HasVote for height 4")
		_, ok = conR.hasVoteEnvelope(vote(5))
		require.True(t, ok)
	})

	t.Run("bench defaults: lazy on, skip-stale off", func(t *testing.T) {
		t.Setenv("CMT_BENCH_LAZY_HASVOTE", "")
		t.Setenv("CMT_BENCH_HASVOTE_SKIP_STALE", "")
		cs, _ := randState(1)
		conR := NewReactor(cs, nil, false)
		require.True(t, conR.lazyHasVote)
		require.False(t, conR.skipStaleHasVote)
		t.Setenv("CMT_BENCH_LAZY_HASVOTE", "0")
		require.False(t, NewReactor(cs, nil, false).lazyHasVote)
	})

	t.Run("knobs from env", func(t *testing.T) {
		t.Setenv("CMT_BENCH_LAZY_HASVOTE", "1")
		t.Setenv("CMT_BENCH_HASVOTE_SKIP_STALE", "1")
		cs, _ := randState(1)
		conR := NewReactor(cs, nil, false)
		require.True(t, conR.lazyHasVote)
		require.True(t, conR.skipStaleHasVote)
	})
}
