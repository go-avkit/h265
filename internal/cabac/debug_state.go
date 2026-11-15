package cabac

// State is a debugging aid: rng, val, n and pos.
func (d *Decoder) State() [4]uint64 { return [4]uint64{uint64(d.rng), d.val, uint64(d.n), uint64(d.pos)} }
