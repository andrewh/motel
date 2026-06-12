package hola

import (
	"math"
	"testing"
)

func TestSolveVPSCUnconstrained(t *testing.T) {
	desired := []float64{3, 1, 2}
	pos := solveVPSC(desired, []float64{1, 1, 1}, nil)
	for i := range desired {
		if math.Abs(pos[i]-desired[i]) > 1e-9 {
			t.Errorf("pos[%d] = %g, want %g", i, pos[i], desired[i])
		}
	}
}

func TestSolveVPSCSeparation(t *testing.T) {
	desired := []float64{0, 0}
	cons := []sepConstraint{{d: dimX, left: 0, right: 1, gap: 10}}
	pos := solveVPSC(desired, []float64{1, 1}, cons)
	if got := pos[1] - pos[0]; got < 10-1e-6 {
		t.Errorf("separation = %g, want >= 10", got)
	}
	// Equal weights: the pair should centre on the desired positions.
	if mid := (pos[0] + pos[1]) / 2; math.Abs(mid) > 1e-6 {
		t.Errorf("midpoint = %g, want 0", mid)
	}
}

func TestSolveVPSCSatisfiedConstraintUntouched(t *testing.T) {
	desired := []float64{0, 100}
	cons := []sepConstraint{{d: dimX, left: 0, right: 1, gap: 10}}
	pos := solveVPSC(desired, []float64{1, 1}, cons)
	if pos[0] != 0 || pos[1] != 100 {
		t.Errorf("pos = %v, want [0 100]", pos)
	}
}

func TestSolveVPSCEquality(t *testing.T) {
	desired := []float64{0, 50}
	cons := []sepConstraint{{d: dimX, left: 0, right: 1, gap: 5, equality: true}}
	pos := solveVPSC(desired, []float64{1, 1}, cons)
	if got := pos[1] - pos[0]; math.Abs(got-5) > 1e-6 {
		t.Errorf("offset = %g, want exactly 5", got)
	}
}

func TestSolveVPSCChain(t *testing.T) {
	desired := []float64{5, 5, 5}
	cons := []sepConstraint{
		{d: dimX, left: 0, right: 1, gap: 4},
		{d: dimX, left: 1, right: 2, gap: 4},
	}
	pos := solveVPSC(desired, []float64{1, 1, 1}, cons)
	if pos[1]-pos[0] < 4-1e-6 || pos[2]-pos[1] < 4-1e-6 {
		t.Errorf("chain not separated: %v", pos)
	}
	if math.Abs(pos[1]-5) > 1e-6 {
		t.Errorf("centre of chain = %g, want 5", pos[1])
	}
}

func TestSolveVPSCBlockSplit(t *testing.T) {
	// Constraint 0-1 gets activated transitively but must split off
	// again once 1-2 pushes 1 away: the optimum leaves 0 at its desired
	// position.
	desired := []float64{0, 1, 2}
	cons := []sepConstraint{
		{d: dimX, left: 0, right: 1, gap: 1},
		{d: dimX, left: 1, right: 2, gap: 10},
	}
	pos := solveVPSC(desired, []float64{1, 1, 1}, cons)
	if pos[1]-pos[0] < 1-1e-6 || pos[2]-pos[1] < 10-1e-6 {
		t.Errorf("constraints violated: %v", pos)
	}
	var cost float64
	for i, p := range pos {
		cost += (p - desired[i]) * (p - desired[i])
	}
	// Optimal: x0=0 free, x1 and x2 share displacement: x1=-3.5, x2=6.5
	// is infeasible against 0-1; optimum is x=[-1.5,-0.5,9.5] with cost
	// 60.75... verify against brute-force bound instead.
	if best := bruteForceCost(desired, cons); cost > best+1e-3 {
		t.Errorf("cost = %g, brute force found %g (pos %v)", cost, best, pos)
	}
}

// bruteForceCost grid-searches the feasible region for a near-optimal
// goal value, as a reference for solver optimality.
func bruteForceCost(desired []float64, cons []sepConstraint) float64 {
	best := math.Inf(1)
	const lo, hi, step = -20.0, 20.0, 0.25
	var rec func(pos []float64)
	rec = func(pos []float64) {
		if len(pos) == len(desired) {
			for _, c := range cons {
				if pos[c.right]-pos[c.left] < c.gap-1e-9 {
					return
				}
				if c.equality && math.Abs(pos[c.right]-pos[c.left]-c.gap) > 1e-9 {
					return
				}
			}
			var cost float64
			for i, p := range pos {
				cost += (p - desired[i]) * (p - desired[i])
			}
			best = math.Min(best, cost)
			return
		}
		for x := lo; x <= hi; x += step {
			rec(append(pos, x))
		}
	}
	rec(make([]float64, 0, len(desired)))
	return best
}
