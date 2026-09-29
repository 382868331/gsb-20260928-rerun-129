// Command perfcore-demo builds traceable execution trees from hand-authored
// samples and compares two program versions. It never reads system processes.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"perfcore/compare"
	"perfcore/demo"
	"perfcore/tree"
)

func main() {
	jsonOut := flag.String("json", "", "write profiles and report as JSON to this file")
	flag.Parse()

	v1, err := tree.Build("run-v1", demo.RunV1()...)
	must(err)
	v2, err := tree.Build("run-v2", demo.RunV2()...)
	must(err)

	fmt.Println("== run v1 (program v1.0.0, symbols sym-v1) ==")
	printTree(v1)
	printReconciliation(v1)

	fmt.Println()
	fmt.Println("== run v2 (program v2.0.0, symbols sym-v2)")
	printTree(v2)
	printReconciliation(v2)

	fmt.Println()
	fmt.Println("== comparison on shared call paths ==")
	printReport(compare.Diff(v1, v2))

	if *jsonOut != "" {
		data, err := json.MarshalIndent(map[string]any{
			"v1": v1, "v2": v2, "diff": compare.Diff(v1, v2),
		}, "", "  ")
		must(err)
		must(os.WriteFile(*jsonOut, data, 0o644))
		fmt.Printf("\nwrote %s\n", *jsonOut)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func nm(n *tree.Node) string {
	if !n.Frame.Resolved {
		return n.Frame.Symbol + " [unresolved]"
	}
	return n.Frame.Symbol
}

func printTree(p *tree.Profile) {
	p.Walk(func(e tree.WalkEntry) {
		indent := strings.Repeat("  ", e.Node.Depth-1)
		rec := ""
		if e.Recurrence > 1 {
			rec = fmt.Sprintf(" (recurrence #%d)", e.Recurrence)
		}
		fmt.Printf("%s- %s%s  self=%d incl=%d\n",
			indent, nm(e.Node), rec, e.Node.SelfWeight, e.Node.InclusiveWeight)
	})
}

func printReconciliation(p *tree.Profile) {
	fmt.Printf("sources=%d samples=%d totalWeight=%d unresolvedSelf=%d\n",
		len(p.Sources), p.SampleCount, p.TotalWeight, p.UnresolvedSelf)
	for _, s := range p.Sources {
		fmt.Printf("  source %s collector=%s program=%s symbols=%s samples=%d weight=%d\n",
			s.BatchID, s.Collector, s.ProgramVersion, s.SymbolVersion, s.Samples, s.Weight)
	}
}

func printReport(r *compare.Report) {
	fmt.Printf("totals: %s=%d -> %s=%d\n", r.FromLabel, r.FromTotal, r.ToLabel, r.ToTotal)
	fmt.Println("shared paths (self weight change):")
	for _, row := range r.Rows {
		fmt.Printf("  %-6s %-28s %2d -> %2d  (%5.1f%% -> %5.1f%%, %+.1fpp) incl %d -> %d\n",
			row.Status, row.Path, row.FromSelf, row.ToSelf,
			row.FromSelfPct, row.ToSelfPct, row.DeltaSelfPct,
			row.FromInclusive, row.ToInclusive)
	}
	for _, row := range r.Added {
		fmt.Printf("  added   %-28s self=%d incl=%d\n", row.Path, row.ToSelf, row.ToInclusive)
	}
	for _, row := range r.Removed {
		fmt.Printf("  removed %-28s self=%d incl=%d\n", row.Path, row.FromSelf, row.FromInclusive)
	}
}
