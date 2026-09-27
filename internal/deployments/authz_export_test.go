package deployments

import "sort"

// MutatingActs lists every Op that changes something, sorted: "every
// operation" for the tests in package deployments_test.
func MutatingActs() []Op { return actsWhere(func(a act) bool { return !a.read }) }

// ReadActs lists the reads, sorted.
func ReadActs() []Op { return actsWhere(func(a act) bool { return a.read }) }

func actsWhere(keep func(act) bool) []Op {
	var out []Op
	for op, a := range acts {
		if keep(a) {
			out = append(out, op)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
