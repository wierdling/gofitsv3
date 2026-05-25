package processing

// AlignmentDiag carries optional alignment-debug state for UI visualization.
type AlignmentDiag struct {
	RefPixels   []float32
	RefW        int
	RefH        int
	RefStars    []Star
	SourceStars []Star
	Pairs       []MatchedPair
}

// AlignmentDebugHook, when set, receives best-effort alignment diagnostics.
// Callers should treat it as optional instrumentation only.
var AlignmentDebugHook func(AlignmentDiag)
