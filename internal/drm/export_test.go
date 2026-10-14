package drm

func Arrays(r *Request) ([]uint32, []uint32, []uint32, []uint64) {
	r.build()
	return r.objects, r.counts, r.props, r.values
}
