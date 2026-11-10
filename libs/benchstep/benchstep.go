// Package benchstep is bench-only: with CELESTIA_BENCH_STEPS_FILE set, it appends one
// JSON line per timed step (height, step, start unix ns, duration µs, count).
package benchstep

import (
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	mu sync.Mutex
	f  *os.File
)

func init() {
	if p := os.Getenv("CELESTIA_BENCH_STEPS_FILE"); p != "" {
		f, _ = os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	}
}

// Since records step at height h, started at t0, with an optional count n.
func Since(h int64, step string, t0 time.Time, n int) {
	if f == nil {
		return
	}
	d := time.Since(t0)
	line := fmt.Sprintf("{\"h\":%d,\"s\":%q,\"t0\":%d,\"us\":%d,\"n\":%d}\n", h, step, t0.UnixNano(), d.Microseconds(), n)
	mu.Lock()
	_, _ = f.WriteString(line)
	mu.Unlock()
}
