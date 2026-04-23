package hygiene

// M2.2.1 T003: the M2.2 palace client (type Client, Query method, plus
// the TimeWindow / Drawer / Triple shapes and all JSON-RPC helpers) was
// relocated to internal/mempalace so the atomic finalize writer in
// internal/spawn can share one code path with the hygiene read path —
// see spec FR-262 and plan §"Changes to existing M2.2 packages >
// internal/mempalace".
//
// This file preserves hygiene's existing public API via type aliases so
// M2.2's callers and tests compile unchanged. Evaluator's public surface
// (EvaluationInput, Evaluate, HygieneStatus) lives in evaluator.go.

import (
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
)

// Client is an alias for mempalace.Client. Preserved for M2.2 callers
// (listener.go, sweep.go, test harness) that construct &hygiene.Client{...}
// directly. From M2.2.1 onwards new call sites should use
// mempalace.Client directly.
type Client = mempalace.Client

// TimeWindow is an alias for mempalace.TimeWindow.
type TimeWindow = mempalace.TimeWindow

// ErrPalaceQueryFailed is the sentinel the hygiene evaluator checks
// via errors.Is. Points at mempalace.ErrQueryFailed — one sentinel,
// two names during the transition window.
var ErrPalaceQueryFailed = mempalace.ErrQueryFailed
