package shell

type rolePhase uint8

const (
	roleFresh rolePhase = iota
	roleAwaitingAck
	roleConfigured
	roleMapped
)

type roleStep uint8

const (
	stepNone roleStep = iota
	stepInitial
	stepMap
	stepUnmap
)

type roleLife struct{ phase rolePhase }

func (r *roleLife) initialSent() bool {
	return r.phase != roleFresh
}

func (r *roleLife) mapped() bool {
	return r.phase == roleMapped
}

func (r *roleLife) acked() {
	if r.phase == roleAwaitingAck {
		r.phase = roleConfigured
	}
}

func (r *roleLife) reset() {
	r.phase = roleFresh
}

func (r *roleLife) commit(hasBuffer bool) (step roleStep, configured bool) {
	switch {
	case hasBuffer && r.phase < roleConfigured:
		return stepNone, false
	case r.phase == roleFresh:
		r.phase = roleAwaitingAck
		return stepInitial, true
	case !hasBuffer && r.phase == roleMapped:
		r.phase = roleFresh
		return stepUnmap, true
	case hasBuffer && r.phase == roleConfigured:
		r.phase = roleMapped
		return stepMap, true
	}
	return stepNone, true
}
