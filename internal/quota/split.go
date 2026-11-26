package quota

import (
	"math"
	"sort"
)

// Split divides total bytes by weight. Shares are integers and sum to total
// (largest-remainder rounding); zero-weight targets get nothing.
func Split(total int64, weights []float64) []int64 {
	out := make([]int64, len(weights))
	if total <= 0 || len(weights) == 0 {
		return out
	}
	var sum float64
	for _, w := range weights {
		if w > 0 {
			sum += w
		}
	}
	if sum <= 0 {
		return out
	}
	type part struct {
		i    int
		frac float64
	}
	parts := make([]part, 0, len(weights))
	var given int64
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		exact := float64(total) * w / sum
		base := int64(math.Floor(exact))
		out[i] = base
		given += base
		parts = append(parts, part{i, exact - float64(base)})
	}
	sort.SliceStable(parts, func(a, b int) bool { return parts[a].frac > parts[b].frac })
	for i := 0; i < int(total-given) && len(parts) > 0; i++ {
		out[parts[i%len(parts)].i]++
	}
	return out
}
