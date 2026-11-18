package propagation

// Bench-only instrumentation for PROTOCO-3058; never part of a PR.

import (
	"github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/trace/schema"
)

func instrNote(blockProp *Reactor, height int64, round int32, kind, format string, items ...interface{}) {
	schema.WriteNote(blockProp.traceClient, height, round, kind, format, items...)
}

// isEdge reports whether index is the first or last original part.
func isEdge(parts *types.CombinedPartSet, index uint32) bool {
	total := parts.Original().Total()
	return index == 0 || index == total-1
}

// edgesIn returns the first and last original indexes set in ba (ba covers
// originals then parity, so the last original is size/2-1).
func edgesIn(ba *bits.BitArray) []int {
	out := []int{}
	if ba == nil || ba.Size() < 2 {
		return out
	}
	last := ba.Size()/2 - 1
	if ba.GetIndex(0) {
		out = append(out, 0)
	}
	if last > 0 && ba.GetIndex(last) {
		out = append(out, last)
	}
	return out
}

func minIndex(h *types.HaveParts) int {
	m := -1
	for _, p := range h.Parts {
		if m < 0 || int(p.Index) < m {
			m = int(p.Index)
		}
	}
	return m
}
