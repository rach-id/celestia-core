package consensus

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/propagation"
	cstypes "github.com/cometbft/cometbft/consensus/types"
	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/types"
)

func TestPacingCompleteProposalWaitsForHeightStart(t *testing.T) {
	cs, _ := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	cs.rs.Proposal = types.NewProposal(cs.rs.Height, 0, -1,
		types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()})
	cs.rs.ProposalBlock = block
	cs.rs.ProposalBlockParts = parts
	cs.rs.StartTime = time.Now().Add(time.Hour)
	cs.rs.Step = cstypes.RoundStepNewHeight
	prevoted := false
	cs.doPrevote = func(int64, int32) { prevoted = true }
	cs.decideProposal = func(int64, int32) {}

	cs.handleCompleteProposal(cs.rs.Height)
	require.False(t, prevoted, "an early proposal must not bypass the height's pacing deadline")
	require.Equal(t, cstypes.RoundStepNewHeight, cs.rs.Step)

	cs.rs.StartTime = time.Now().Add(-time.Second)
	cs.enterNewRound(cs.rs.Height, 0)
	require.True(t, prevoted, "the cached proposal must be processed when the height starts")
	require.Equal(t, cstypes.RoundStepPrevote, cs.rs.Step)
}

func TestPacingEarlyRoundZeroWaitsForHeightStart(t *testing.T) {
	cs, _ := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	cs.SetPrivValidator(nil)
	cs.rs.StartTime = time.Now().Add(time.Hour)

	cs.enterNewRound(cs.rs.Height, 0)

	require.Equal(t, cstypes.RoundStepNewHeight, cs.rs.Step,
		"enterNewRound must preserve the pacing deadline when called before its timeout")
}

func TestPacingUsesLocalStartAcrossValidatorClockOffsets(t *testing.T) {
	base := time.Now().Add(time.Hour)
	for _, offset := range []time.Duration{-2 * time.Second, 0, 2 * time.Second} {
		t.Run(offset.String(), func(t *testing.T) {
			localStart := base.Add(offset)
			localCommit := localStart.Add(400 * time.Millisecond)
			// Every validator receives the same signed proposer timestamp. Its
			// own clock offset must not change the elapsed pacing interval.
			cs := pacingCommittedState(t, localStart, localCommit, base.Add(300*time.Millisecond))
			nextState := cs.state.Copy()
			nextState.LastBlockHeight = cs.rs.Height
			nextState.LastValidators = cs.rs.Validators

			cs.updateToState(nextState)

			require.Equal(t, time.Second, cs.rs.StartTime.Sub(localStart),
				"proposal construction and a remote validator's clock must not alter the height interval")
		})
	}
}

func TestPacingSlowFinalizationDoesNotLeaveStaleStart(t *testing.T) {
	start := time.Now().Add(-5 * time.Second)
	cs := pacingCommittedState(t, start, start.Add(100*time.Millisecond), start)
	nextState := cs.state.Copy()
	nextState.LastBlockHeight = cs.rs.Height
	nextState.LastValidators = cs.rs.Validators
	finalizationComplete := time.Now()

	cs.updateToState(nextState)

	require.False(t, cs.rs.StartTime.Before(finalizationComplete),
		"a long FinalizeBlock must not leave a stale anchor that lets the following height run early")
}

func TestPacingCommitBeforeScheduledStartDoesNotAccumulateDelay(t *testing.T) {
	now := time.Now()
	cs := pacingCommittedState(t, now.Add(10*time.Second), now, now.Add(10*time.Second))
	nextState := cs.state.Copy()
	nextState.LastBlockHeight = cs.rs.Height
	nextState.LastValidators = cs.rs.Validators

	cs.updateToState(nextState)

	require.False(t, cs.rs.StartTime.After(time.Now().Add(time.Second)),
		"committing before a scheduled start must not carry an unused deadline into later heights")
}

func TestPacingFirstCommitAlignsLocalStartupTimes(t *testing.T) {
	commit := time.Now().Add(time.Hour)
	for _, offset := range []time.Duration{-2 * time.Second, -200 * time.Millisecond, 200 * time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			cs := pacingCommittedState(t, commit.Add(offset), commit, commit.Add(-time.Hour))
			cs.pacingStarted = false
			nextState := cs.state.Copy()
			nextState.LastBlockHeight = cs.rs.Height
			nextState.LastValidators = cs.rs.Validators

			cs.updateToState(nextState)

			require.Equal(t, time.Second, cs.rs.StartTime.Sub(commit),
				"startup cadence must anchor on a common observed event, not each validator's startup time")
		})
	}
}

func TestPacingPrecommitQuorumStartsRoundZeroBeforeDeadline(t *testing.T) {
	cs, vss := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	cs.SetPrivValidator(nil)
	cs.decideProposal = func(int64, int32) {}
	cs.rs.StartTime = time.Now().Add(time.Hour)
	pacingAddPrecommitQuorum(t, cs, vss, 0)

	cs.enterNewRound(cs.rs.Height, 0)

	require.Equal(t, cstypes.RoundStepPropose, cs.rs.Step,
		"a committing quorum for the height must not be held by the floor")
}

func TestPacingMultiRoundHeightStartsNextWithinFloorOfCommit(t *testing.T) {
	const timeoutCommit = time.Second
	for name, tc := range map[string]struct{ start, commit time.Duration }{
		"rounds overran the floor": {start: -3 * time.Second, commit: -100 * time.Millisecond},
		"rounds within the floor":  {start: -300 * time.Millisecond, commit: -50 * time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			cs, vss := randState(4)
			t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
			cs.state.Timeouts.TimeoutCommit = timeoutCommit
			cs.pacingStarted = true
			cs.rs.StartTime = now.Add(tc.start)
			commit := now.Add(tc.commit)

			pacingCommitHeight(t, cs, vss, 1, commit)

			require.False(t, cs.rs.StartTime.Before(commit), "the next height must not start before the commit")
			require.False(t, cs.rs.StartTime.After(commit.Add(timeoutCommit)),
				"a multi-round height must not delay the next one by more than TimeoutCommit")
		})
	}
}

func TestPacingForwardClockJumpRestartsCadence(t *testing.T) {
	const timeoutCommit = time.Second
	cs, vss := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	cs.state.Timeouts.TimeoutCommit = timeoutCommit
	cs.pacingStarted = true
	// The wall clock jumped an hour forward after the height started.
	cs.rs.StartTime = time.Now().Add(-time.Hour)
	before := time.Now()

	pacingCommitHeight(t, cs, vss, 0, cs.rs.StartTime.Add(100*time.Millisecond))

	require.False(t, cs.rs.StartTime.Before(before), "a stale deadline must not be carried past the jump")
	require.False(t, cs.rs.StartTime.After(time.Now()), "the jump must not add a wait")
	jumped := cs.rs.StartTime

	pacingCommitHeight(t, cs, vss, 0, jumped.Add(100*time.Millisecond))

	require.Equal(t, jumped.Add(timeoutCommit), cs.rs.StartTime,
		"the cadence must resume from the post-jump start without a catch-up burst")
}

func TestPacingAnyPrecommitQuorumStartsRoundZeroBeforeDeadline(t *testing.T) {
	cs, vss := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	cs.SetPrivValidator(nil)
	cs.decideProposal = func(int64, int32) {}
	recorder := &recordingPropagator{NoOpPropagator: propagation.NewNoOpPropagator()}
	cs.propagator = recorder
	cs.rs.StartTime = time.Now().Add(time.Hour)
	ext := cs.state.ConsensusParams.ABCI.VoteExtensionsEnabled(cs.rs.Height)
	header := types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(tmhash.Size)}
	for _, vs := range vss[1:] {
		vs.Height, vs.Round = cs.rs.Height, 0
	}
	// +2/3 precommits without a majority: two for a block, one for nil.
	votes := signVotes(cmtproto.PrecommitType, cmtrand.Bytes(tmhash.Size), header, ext, vss[1:3]...)
	votes = append(votes, signVotes(cmtproto.PrecommitType, nil, types.PartSetHeader{}, ext, vss[3])...)
	for _, vote := range votes {
		_, err := cs.addVote(vote, "peer")
		require.NoError(t, err)
	}

	calls := recorder.snapshotCalls()
	require.NotEmpty(t, calls, "round 0 must start before moving to the precommit wait")
	require.Equal(t, proposerAndRoundCall{height: cs.rs.Height, round: 0, proposer: calls[0].proposer}, calls[0])
	require.Equal(t, cstypes.RoundStepPrecommitWait, cs.rs.Step)
}

func TestPacingHeldProposalPrevotedWhenWaitingForTxs(t *testing.T) {
	cs, _ := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	cs.config.CreateEmptyBlocks = false
	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	cs.state.InitialHeight = cs.rs.Height + 100
	cs.blockStore = pacingNoProofStore{BlockStore: cs.blockStore, appHash: cs.state.AppHash}
	require.False(t, cs.needProofBlock(cs.rs.Height))
	cs.rs.Proposal = types.NewProposal(cs.rs.Height, 0, -1,
		types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()})
	cs.rs.ProposalBlock = block
	cs.rs.ProposalBlockParts = parts
	cs.rs.StartTime = time.Now().Add(time.Hour)
	prevoted := false
	cs.doPrevote = func(int64, int32) { prevoted = true }
	cs.decideProposal = func(int64, int32) {}

	cs.handleCompleteProposal(cs.rs.Height)
	require.False(t, prevoted)
	cs.rs.StartTime = time.Now().Add(-time.Second)
	cs.enterNewRound(cs.rs.Height, 0)

	require.True(t, prevoted, "a held proposal must not wait for mempool txs once the round starts")
}

// pacingNoProofStore reports an unchanged app hash, so no proof block is needed.
type pacingNoProofStore struct {
	sm.BlockStore
	appHash []byte
}

func (s pacingNoProofStore) LoadBlockMeta(int64) *types.BlockMeta {
	return &types.BlockMeta{Header: types.Header{AppHash: s.appHash}}
}

func TestPacingBackwardClockStepBoundsDeferral(t *testing.T) {
	cs, _ := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	cs.SetPrivValidator(nil)
	cs.state.Timeouts.TimeoutCommit = time.Second
	// A start time an hour ahead models the wall clock stepping back an hour.
	cs.rs.StartTime = time.Now().Add(time.Hour)

	cs.enterNewRound(cs.rs.Height, 0)

	require.Equal(t, cstypes.RoundStepNewHeight, cs.rs.Step)
	require.False(t, cs.rs.StartTime.After(time.Now().Add(time.Second)),
		"round 0 must not be held longer than one TimeoutCommit")
}

// pacingAddPrecommitQuorum adds +2/3 precommits for the current height.
func pacingAddPrecommitQuorum(t *testing.T, cs *State, vss []*validatorStub, round int32) {
	t.Helper()
	ext := cs.state.ConsensusParams.ABCI.VoteExtensionsEnabled(cs.rs.Height)
	cs.rs.Votes.SetRound(round)
	header := types.PartSetHeader{Total: 1, Hash: cmtrand.Bytes(tmhash.Size)}
	for _, vs := range vss[1:] {
		vs.Height, vs.Round = cs.rs.Height, round
	}
	for _, vote := range signVotes(cmtproto.PrecommitType, cmtrand.Bytes(tmhash.Size), header, ext, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", ext)
		require.NoError(t, err)
		require.True(t, added)
	}
}

// pacingCommitHeight commits the current height at round and commit time,
// then moves to the next height as finalizeCommit does.
func pacingCommitHeight(t *testing.T, cs *State, vss []*validatorStub, round int32, commit time.Time) {
	t.Helper()
	pacingAddPrecommitQuorum(t, cs, vss, round)
	cs.rs.CommitRound = round
	cs.rs.CommitTime = commit
	nextState := cs.state.Copy()
	nextState.LastBlockHeight = cs.rs.Height
	nextState.LastValidators = cs.rs.Validators
	cs.updateToState(nextState)
}

func TestPacingAllLastCommitVotesDoNotSkipHeightStart(t *testing.T) {
	cs, vss := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	for _, vote := range signVotes(cmtproto.PrecommitType, block.Hash(), parts.Header(), true, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}
	vss[0].Height = cs.rs.Height
	vote, err := vss[0].signVote(cmtproto.PrecommitType, block.Hash(), parts.Header(), nil, true)
	require.NoError(t, err)
	cs.rs.LastCommit = cs.rs.Votes.Precommits(0)
	cs.rs.Height++
	cs.rs.Votes = cstypes.NewHeightVoteSet(cs.state.ChainID, cs.rs.Height, cs.rs.Validators)
	cs.rs.StartTime = time.Now().Add(time.Hour)
	cs.rs.Step = cstypes.RoundStepNewHeight
	cs.config.SkipTimeoutCommit = true //nolint:staticcheck // the ignored setting must not skip the floor
	cs.SetPrivValidator(nil)

	added, err := cs.addVote(vote, "peer")

	require.NoError(t, err)
	require.True(t, added)
	require.True(t, cs.rs.LastCommit.HasAll())
	require.Equal(t, cstypes.RoundStepNewHeight, cs.rs.Step,
		"collecting all previous precommits must not bypass the block interval")
}

func pacingCommittedState(t *testing.T, start, commit, proposalTime time.Time) *State {
	t.Helper()
	cs, vss := randState(4)
	t.Cleanup(func() { require.NoError(t, cs.eventBus.Stop()) })
	cs.state.Timeouts.TimeoutCommit = time.Second
	cs.config.TimeoutCommit = time.Second
	cs.pacingStarted = true
	block, parts, err := cs.createProposalBlock(context.Background())
	require.NoError(t, err)
	blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
	proposal := types.NewProposal(cs.rs.Height, 0, -1, blockID)
	proposal.Timestamp = proposalTime
	p := proposal.ToProto()
	require.NoError(t, cs.privValidator.SignProposal(cs.state.ChainID, p))
	proposal.Signature = p.Signature
	require.NoError(t, cs.defaultSetProposal(proposal))
	for _, vote := range signVotes(cmtproto.PrecommitType, blockID.Hash, blockID.PartSetHeader, true, vss[1:]...) {
		added, err := cs.rs.Votes.AddVote(vote, "peer", true)
		require.NoError(t, err)
		require.True(t, added)
	}
	cs.rs.StartTime = start
	cs.rs.CommitTime = commit
	cs.rs.CommitRound = 0
	return cs
}
