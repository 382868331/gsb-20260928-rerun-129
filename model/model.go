// Package model defines the collector-independent sampling data model.
//
// Collectors of any kind (synthetic, CPU, wall-clock, ...) only need to emit
// batches of samples in this shape. Every downstream analysis result is built
// from these types, so adding a new collector never requires changing the
// analysis core.
package model

// Frame is one frame on a sampled call stack.
//
// Key is the stable identity used to merge frames (function name, mangled
// symbol, raw PC, ...). Symbol is the human-readable name. When a frame cannot
// be symbolized the collector must still emit it: set Resolved=false and use a
// stable Key such as the raw program counter so that no sample is dropped.
type Frame struct {
	Key           string `json:"key"`
	Symbol        string `json:"symbol"`
	Resolved      bool   `json:"resolved"`
	Location      string `json:"location,omitempty"`
	SymbolVersion string `json:"symbol_version,omitempty"`
}

// Sample is one weighted stack observation.
//
// Stack is ordered leaf-first (innermost/active frame at index 0, outermost
// frame last), matching the convention used by common sampling profilers.
// Weight is the count this observation contributes (normally 1; merged or
// weighted samples may carry larger values).
type Sample struct {
	ID      string  `json:"id"`
	BatchID string  `json:"batch_id"`
	Stack   []Frame `json:"stack"`
	Weight  int64   `json:"weight"`
}

// Batch is a group of samples coming from one collector run.
// Provenance (collector, program build, symbol table) is kept here and on the
// frames, so analysis results always carry their source and symbol version.
type Batch struct {
	ID               string   `json:"id"`
	Collector        string   `json:"collector"`
	CollectorVersion string   `json:"collector_version,omitempty"`
	ProgramVersion   string   `json:"program_version"`
	SymbolVersion    string   `json:"symbol_version,omitempty"`
	Samples          []Sample `json:"samples"`
}

// Source records where a merged profile came from.
type Source struct {
	BatchID          string `json:"batch_id"`
	Collector        string `json:"collector"`
	CollectorVersion string `json:"collector_version,omitempty"`
	ProgramVersion   string `json:"program_version"`
	SymbolVersion    string `json:"symbol_version,omitempty"`
	Samples          int    `json:"samples"`
	Weight           int64  `json:"weight"`
}
