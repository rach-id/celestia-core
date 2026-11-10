package privval

import (
	"os"
	"strconv"
	"strings"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

const maxBenchSignDelay = 10 * time.Second

// benchSignDelayFile holds a vote-signing delay in ms, re-read on every vote so
// it can change without a restart. Bench-only; missing or invalid means 0.
var benchSignDelayFile = func() string {
	if p := os.Getenv("CELESTIA_BENCH_SIGN_DELAY_FILE"); p != "" {
		return p
	}
	return "/root/sign_delay_ms"
}()

func benchSignDelay() time.Duration {
	b, err := os.ReadFile(benchSignDelayFile)
	if err != nil {
		return 0
	}
	ms, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || ms <= 0 {
		return 0
	}
	return min(time.Duration(ms)*time.Millisecond, maxBenchSignDelay)
}

func benchSleepBeforeVote(vote *cmtproto.Vote) {
	if vote.Type != cmtproto.PrevoteType && vote.Type != cmtproto.PrecommitType {
		return
	}
	if d := benchSignDelay(); d > 0 {
		time.Sleep(d)
	}
}
