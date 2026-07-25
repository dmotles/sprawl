package perfstat

// CacheStats is a plain snapshot of render-cache accounting. perfstat never
// reads ChatList: the caller populates these scalars from whatever accessors it
// has and passes the struct in by value.
//
// Hits and Misses are absolute lifetime counters, not per-frame deltas. The
// detector derives deltas itself and tolerates the counters restarting.
//
// They must come from the outer, (width, revision)-keyed whole-chat render
// cache, which is what Revision and Width fingerprint. Feeding counters from a
// cache keyed on anything else — for example a per-item cache that also keys on
// an expand/collapse flag — would make an unrelated key change look like a
// rebuild with the revision pinned, i.e. a false positive.
type CacheStats struct {
	Hits        uint64 // whole-chat render-cache hits
	Misses      uint64 // whole-chat render-cache rebuilds
	Items       int    // total chat items
	Uncacheable int    // items that cannot be cached because they never finished
	Revision    uint64 // cache-invalidation fingerprint at sample time
	Width       int    // render width the sample was taken at
}

// Lookups reports the total number of cache consultations.
func (c CacheStats) Lookups() uint64 { return c.Hits + c.Misses }

// HitRate reports hits as a fraction of lookups, or 0 when nothing was looked up.
func (c CacheStats) HitRate() float64 {
	lookups := c.Lookups()
	if lookups == 0 {
		return 0
	}
	return float64(c.Hits) / float64(lookups)
}
