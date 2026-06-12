package hola

// Port of the libvpsc solver: projects desired positions onto separation
// and alignment constraints by merging variables into blocks along active
// constraints, then splitting blocks whose active constraints have
// negative Lagrange multipliers ("Fast Node Overlap Removal", Dwyer,
// Marriott and Stuckey, 2005).

type dim int

const (
	dimX dim = iota
	dimY
)

func (d dim) other() dim {
	if d == dimX {
		return dimY
	}
	return dimX
}

// sepConstraint requires pos(right) >= pos(left) + gap in dimension d,
// or pos(right) == pos(left) + gap when equality is set.
type sepConstraint struct {
	d           dim
	left, right int
	gap         float64
	equality    bool
}

type vpscVar struct {
	desired float64
	weight  float64
	block   *vpscBlock
	offset  float64
	in, out []*vpscConstraint
}

func (v *vpscVar) pos() float64 { return v.block.posn + v.offset }

type vpscConstraint struct {
	left, right *vpscVar
	gap         float64
	equality    bool
	active      bool
	skipped     bool
	lm          float64
}

func (c *vpscConstraint) violation() float64 {
	return c.left.pos() + c.gap - c.right.pos()
}

type vpscBlock struct {
	vars []*vpscVar
	posn float64
}

func (b *vpscBlock) updatePosn() {
	var wsum, wdsum float64
	for _, v := range b.vars {
		wsum += v.weight
		wdsum += v.weight * (v.desired - v.offset)
	}
	b.posn = wdsum / wsum
}

// dfdv computes the derivative of the goal function with respect to each
// variable in the block, recording Lagrange multipliers on the active
// constraints traversed. The active constraints in a block always form a
// tree, so the recursion terminates.
func (b *vpscBlock) dfdv(v *vpscVar, skip *vpscConstraint) float64 {
	df := v.weight * (v.pos() - v.desired)
	for _, c := range v.out {
		if c.active && c != skip {
			c.lm = b.dfdv(c.right, c)
			df += c.lm
		}
	}
	for _, c := range v.in {
		if c.active && c != skip {
			c.lm = -b.dfdv(c.left, c)
			df -= c.lm
		}
	}
	return df
}

const vpscEps = 1e-8

// solveVPSC returns positions minimising sum w_i*(pos_i - desired_i)^2
// subject to the constraints. Constraint indices refer to the desired
// slice. Unsatisfiable constraints (cycles) are dropped.
func solveVPSC(desired, weight []float64, cons []sepConstraint) []float64 {
	n := len(desired)
	vars := make([]*vpscVar, n)
	for i := range vars {
		v := &vpscVar{desired: desired[i], weight: weight[i]}
		v.block = &vpscBlock{vars: []*vpscVar{v}, posn: desired[i]}
		vars[i] = v
	}
	cs := make([]*vpscConstraint, 0, len(cons))
	for _, sc := range cons {
		if sc.left == sc.right {
			continue
		}
		c := &vpscConstraint{
			left:     vars[sc.left],
			right:    vars[sc.right],
			gap:      sc.gap,
			equality: sc.equality,
		}
		c.left.out = append(c.left.out, c)
		c.right.in = append(c.right.in, c)
		cs = append(cs, c)
	}

	maxIters := 10*(n+len(cs)) + 100
	for iter := 0; iter < maxIters; iter++ {
		if c := mostViolated(cs); c != nil {
			if c.left.block == c.right.block {
				c.skipped = true
				continue
			}
			mergeBlocks(c)
			continue
		}
		if !splitOnce(cs) {
			break
		}
	}

	out := make([]float64, n)
	for i, v := range vars {
		out[i] = v.pos()
	}
	return out
}

func mostViolated(cs []*vpscConstraint) *vpscConstraint {
	var worst *vpscConstraint
	worstV := vpscEps
	for _, c := range cs {
		if c.skipped || c.active {
			continue
		}
		v := c.violation()
		if c.equality {
			if v < 0 {
				v = -v
			}
		}
		if v > worstV {
			worstV = v
			worst = c
		}
	}
	return worst
}

func mergeBlocks(c *vpscConstraint) {
	lb, rb := c.left.block, c.right.block
	if len(rb.vars) > len(lb.vars) {
		// Absorb the smaller block: shift the left block so that the
		// constraint holds with equality, expressed in rb coordinates.
		shift := c.right.offset - c.gap - c.left.offset
		for _, v := range lb.vars {
			v.offset += shift
			v.block = rb
		}
		rb.vars = append(rb.vars, lb.vars...)
		rb.updatePosn()
	} else {
		shift := c.left.offset + c.gap - c.right.offset
		for _, v := range rb.vars {
			v.offset += shift
			v.block = lb
		}
		lb.vars = append(lb.vars, rb.vars...)
		lb.updatePosn()
	}
	c.active = true
}

// splitOnce finds the active non-equality constraint with the most
// negative Lagrange multiplier and removes it, splitting its block into
// two. Returns false when no constraint wants to split.
func splitOnce(cs []*vpscConstraint) bool {
	seen := make(map[*vpscBlock]bool)
	for _, c := range cs {
		if c.active && !seen[c.left.block] {
			b := c.left.block
			seen[b] = true
			b.dfdv(b.vars[0], nil)
		}
	}
	var worst *vpscConstraint
	worstLM := -vpscEps
	for _, c := range cs {
		if c.active && !c.equality && c.lm < worstLM {
			worstLM = c.lm
			worst = c
		}
	}
	if worst == nil {
		return false
	}
	worst.active = false
	splitBlock(worst)
	return true
}

func splitBlock(c *vpscConstraint) {
	for _, root := range []*vpscVar{c.left, c.right} {
		b := &vpscBlock{}
		collectComponent(root, nil, b)
		for _, v := range b.vars {
			v.block = b
		}
		b.updatePosn()
	}
}

func collectComponent(v *vpscVar, from *vpscConstraint, b *vpscBlock) {
	b.vars = append(b.vars, v)
	for _, c := range v.out {
		if c.active && c != from {
			collectComponent(c.right, c, b)
		}
	}
	for _, c := range v.in {
		if c.active && c != from {
			collectComponent(c.left, c, b)
		}
	}
}
