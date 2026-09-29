package demo

import "perfcore/model"

func frame(symbol string) model.Frame {
	return model.Frame{Key: symbol, Symbol: symbol, Resolved: true}
}

// unresolved emits a frame whose symbol could not be resolved. The stable key
// (e.g. raw PC) keeps the sample attributable without dropping it.
func unresolved(key string) model.Frame {
	return model.Frame{Key: key, Symbol: "?" + key, Resolved: false, Location: key}
}

// stack lists frames outer-first for readability and reverses to leaf-first.
func stack(frames ...model.Frame) []model.Frame {
	out := make([]model.Frame, len(frames))
	for i, f := range frames {
		out[len(frames)-1-i] = f
	}
	return out
}

// RunV1 and RunV2 are the two program versions compared by the demo.
// They intentionally share most call paths so changes are visible on the same
// paths. The data is hand-authored; no system process is ever sampled.
func RunV1() []model.Batch {
	sample := func(id string, w int64, st []model.Frame) model.Sample {
		return model.Sample{ID: id, BatchID: "b1", Stack: st, Weight: w}
	}
	main := frame("main")
	handle := frame("handleRequest")
	parse := frame("parseQuery")
	fetch := frame("fetchUser")
	validate := frame("validate")
	fib := frame("fib")

	b1 := model.Batch{
		ID: "v1-batch-1", Collector: "synthetic-manual", CollectorVersion: "demo-1",
		ProgramVersion: "v1.0.0", SymbolVersion: "sym-v1",
		Samples: []model.Sample{
			// 3 samples leaf in parseQuery (direct/self time).
			sample("s1", 1, stack(main, handle, parse)),
			sample("s2", 1, stack(main, handle, parse)),
			sample("s3", 1, stack(main, handle, parse)),
			// 2 samples leaf in fetchUser.
			sample("s4", 1, stack(main, handle, fetch)),
			sample("s5", 1, stack(main, handle, fetch)),
			// 1 sample leaf in validate.
			sample("s6", 1, stack(main, handle, validate)),
			// Recursion fib->fib->fib: three distinct occurrences, one leaf.
			sample("s7", 2, stack(main, fib, fib, fib)),
			// One unresolved frame leaf: sample survives symbolization failure.
			sample("s8", 1, stack(main, handle, unresolved("0x4010a9"))),
		},
	}
	// A second batch delivered later, exercising batch merging.
	b2 := b1
	b2.ID = "v1-batch-2"
	b2.Samples = append([]model.Sample(nil), b1.Samples...)
	for i := range b2.Samples {
		b2.Samples[i].ID = b2.Samples[i].ID + "b"
		b2.Samples[i].BatchID = b2.ID
	}
	return []model.Batch{b1, b2}
}

// RunV2 reuses the same paths but shifts weight: parseQuery got cheaper,
// fetchUser got more expensive, validate disappeared, a cache path was added.
func RunV2() []model.Batch {
	sample := func(id string, w int64, st []model.Frame) model.Sample {
		return model.Sample{ID: id, BatchID: "b3", Stack: st, Weight: w}
	}
	main := frame("main")
	handle := frame("handleRequest")
	parse := frame("parseQuery")
	fetch := frame("fetchUser")
	cache := frame("cacheLookup")
	fib := frame("fib")

	b := model.Batch{
		ID: "v2-batch-1", Collector: "synthetic-manual", CollectorVersion: "demo-1",
		ProgramVersion: "v2.0.0", SymbolVersion: "sym-v2",
		Samples: []model.Sample{
			sample("t1", 1, stack(main, handle, parse)),
			sample("t2", 1, stack(main, handle, fetch)),
			sample("t3", 1, stack(main, handle, fetch)),
			sample("t4", 1, stack(main, handle, fetch)),
			sample("t5", 1, stack(main, handle, fetch, cache)),
			sample("t6", 2, stack(main, fib, fib, fib)),
			sample("t7", 1, stack(main, handle, unresolved("0x4010a9"))),
		},
	}
	return []model.Batch{b}
}
