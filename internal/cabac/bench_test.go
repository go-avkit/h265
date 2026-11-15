package cabac

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// Bins per second, for the engine and for the bin-serial reference of the
// specification (ref_test.go, which does what the text says one bit at a time),
// on the same random data. Run on a quiet machine, one core:
//
//	go test -run '^$' -bench . -benchtime 2s -count 5 ./internal/cabac
//
// Each benchmark op decodes perOp bins; Mbins/s is the figure to compare.

const perOp = 1024

func benchData() []byte {
	rng := rand.New(rand.NewPCG(99, 100))
	data := randomData(rng, 1<<22)
	data[0] &= 0x7F
	return data
}

func report(b *testing.B, binsPerOp int) {
	b.ReportMetric(float64(b.N)*float64(binsPerOp)/b.Elapsed().Seconds()/1e6, "Mbins/s")
}

func BenchmarkDecision(b *testing.B) {
	data := benchData()
	b.Run("engine", func(b *testing.B) {
		var d Decoder
		_ = d.Init(data)
		var ctx [16]uint8
		for i := range ctx {
			ctx[i] = uint8(i) << 1
		}
		b.ResetTimer()
		for n := 0; n < b.N; n++ {
			if d.BitPos() > int64(len(data))*8-1<<16 {
				_ = d.Init(data)
			}
			for i := 0; i < perOp; i++ {
				d.Decision(&ctx[i&15])
			}
		}
		report(b, perOp)
	})
	b.Run("serial", func(b *testing.B) {
		r, _ := newRefDecoder(data)
		var ctx [16]refCtx
		b.ResetTimer()
		for n := 0; n < b.N; n++ {
			if r.pos > len(data)*8-1<<16 {
				r, _ = newRefDecoder(data)
			}
			for i := 0; i < perOp; i++ {
				r.decision(&ctx[i&15])
			}
		}
		report(b, perOp)
	})
}

// skewedDecisions is a stream of decisions written by the informative encoder
// (9.3.5), the way coded video looks to the decoder: sixteen contexts taken in
// turn, each bin the MPS with probability 0.92, so the contexts climb to skewed
// states and the LPS is rare. The branch predictor does far better on this than
// on random data.
func skewedDecisions(n int) (data []byte, start [16]uint8) {
	rng := rand.New(rand.NewPCG(101, 102))
	var rc [16]refCtx
	for i := range rc {
		rc[i] = refCtx{rng.IntN(20), rng.IntN(2)}
		start[i] = rc[i].byteForm()
	}
	enc := newRefEncoder()
	for i := 0; i < n; i++ {
		c := &rc[i&15]
		bin := c.valMps
		if rng.IntN(100) >= 92 {
			bin ^= 1
		}
		enc.decision(c, bin)
	}
	enc.terminate(1)
	return enc.packed(), start
}

func BenchmarkDecisionSkewed(b *testing.B) {
	const total = 1 << 21
	data, start := skewedDecisions(total)
	b.Run("engine", func(b *testing.B) {
		var d Decoder
		_ = d.Init(data)
		ctx, done := start, 0
		b.ResetTimer()
		for n := 0; n < b.N; n++ {
			if done+perOp > total {
				_ = d.Init(data)
				ctx, done = start, 0
			}
			for i := 0; i < perOp; i++ {
				d.Decision(&ctx[i&15])
			}
			done += perOp
		}
		report(b, perOp)
	})
	b.Run("serial", func(b *testing.B) {
		r, _ := newRefDecoder(data)
		var rc [16]refCtx
		for i := range rc {
			rc[i] = refCtxOf(start[i])
		}
		done := 0
		b.ResetTimer()
		for n := 0; n < b.N; n++ {
			if done+perOp > total {
				r, _ = newRefDecoder(data)
				for i := range rc {
					rc[i] = refCtxOf(start[i])
				}
				done = 0
			}
			for i := 0; i < perOp; i++ {
				r.decision(&rc[i&15])
			}
			done += perOp
		}
		report(b, perOp)
	})
}

func BenchmarkBypass(b *testing.B) {
	data := benchData()
	b.Run("engine", func(b *testing.B) {
		var d Decoder
		_ = d.Init(data)
		b.ResetTimer()
		for n := 0; n < b.N; n++ {
			if d.BitPos() > int64(len(data))*8-1<<16 {
				_ = d.Init(data)
			}
			for i := 0; i < perOp; i++ {
				d.Bypass()
			}
		}
		report(b, perOp)
	})
	b.Run("serial", func(b *testing.B) {
		r, _ := newRefDecoder(data)
		b.ResetTimer()
		for n := 0; n < b.N; n++ {
			if r.pos > len(data)*8-1<<16 {
				r, _ = newRefDecoder(data)
			}
			for i := 0; i < perOp; i++ {
				r.bypass()
			}
		}
		report(b, perOp)
	})
}

// BenchmarkBypassRun decodes runs of k bypass bins: the engine's one-division
// BypassBits, the engine's Bypass called k times, and the serial reference.
func BenchmarkBypassRun(b *testing.B) {
	data := benchData()
	for _, k := range []uint{2, 4, 8, 16, 23, 32} {
		runs := perOp / int(k)
		bins := runs * int(k)
		b.Run(fmt.Sprintf("k=%d/engine-run", k), func(b *testing.B) {
			var d Decoder
			_ = d.Init(data)
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				if d.BitPos() > int64(len(data))*8-1<<16 {
					_ = d.Init(data)
				}
				for i := 0; i < runs; i++ {
					d.BypassBits(k)
				}
			}
			report(b, bins)
		})
		b.Run(fmt.Sprintf("k=%d/engine-loop", k), func(b *testing.B) {
			var d Decoder
			_ = d.Init(data)
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				if d.BitPos() > int64(len(data))*8-1<<16 {
					_ = d.Init(data)
				}
				for i := 0; i < runs; i++ {
					var v uint32
					for j := uint(0); j < k; j++ {
						v = v<<1 | d.Bypass()
					}
				}
			}
			report(b, bins)
		})
		b.Run(fmt.Sprintf("k=%d/serial", k), func(b *testing.B) {
			r, _ := newRefDecoder(data)
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				if r.pos > len(data)*8-1<<16 {
					r, _ = newRefDecoder(data)
				}
				for i := 0; i < runs; i++ {
					r.bypassBits(int(k))
				}
			}
			report(b, bins)
		})
	}
}
