// xoshiro256** is the Go-side counterpart of the JS rng.js. Both share
// the same algorithm so that - given the same seed - the *intent*
// stream of orders is identical regardless of which language the bot
// is written in. (Wire-level reproducibility additionally requires the
// system clock and network conditions to match, which is documented in
// BLUEPRINT.md §"Determinism".)
package bot

type xoshiro struct {
	s [4]uint64
}

func newXoshiro(seed uint64) *xoshiro {
	x := &xoshiro{}
	// splitmix64 to expand a 64-bit seed into 4×64-bit state.
	s := seed
	for i := 0; i < 4; i++ {
		s += 0x9E3779B97F4A7C15
		z := s
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		x.s[i] = z ^ (z >> 31)
		if x.s[i] == 0 {
			x.s[i] = 1
		}
	}
	return x
}

func (x *xoshiro) next() uint64 {
	rotl := func(x uint64, k uint) uint64 { return (x << k) | (x >> (64 - k)) }
	result := rotl(x.s[1]*5, 7) * 9
	t := x.s[1] << 17
	x.s[2] ^= x.s[0]
	x.s[3] ^= x.s[1]
	x.s[1] ^= x.s[2]
	x.s[0] ^= x.s[3]
	x.s[2] ^= t
	x.s[3] = rotl(x.s[3], 45)
	return result
}
