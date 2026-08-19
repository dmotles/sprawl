package usage

// costEpsilon bounds float comparison of dollar amounts. Deltas are built by
// subtraction, so exact equality is not safe even for tidy decimal inputs. It
// sits well below costNoiseTolerance, so a comparison here can never mask the
// noise-vs-reset distinction deltaFrom draws.
const costEpsilon = 1e-9

// closeTo is the shared cost comparison for this package's tests. It lives in
// its own file rather than alongside one test group because several use it.
func closeTo(got, want float64) bool {
	d := got - want
	return d < costEpsilon && d > -costEpsilon
}
