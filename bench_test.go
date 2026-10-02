package main

import (
	"io"
	"os"
	"runtime"
	"testing"
)

// Benchmarks against a real repository, e.g.:
//
//	git clone --depth 1 https://github.com/kubernetes/kubernetes /tmp/k8s
//	SPROUT_BENCH_DIR=/tmp/k8s go test -run '^$' -bench .
func benchRun(b *testing.B, args ...string) {
	dir := os.Getenv("SPROUT_BENCH_DIR")
	if dir == "" {
		b.Skip("set SPROUT_BENCH_DIR to a large repository")
	}
	for i := 0; i < b.N; i++ {
		if code := run(append([]string{dir, "--no-config"}, args...), io.Discard, io.Discard); code != 0 {
			b.Fatalf("exit %d", code)
		}
	}
}

func BenchmarkTree(b *testing.B)       { benchRun(b) }
func BenchmarkTreeDepth2(b *testing.B) { benchRun(b, "-L", "2") }
func BenchmarkJSON(b *testing.B)       { benchRun(b, "--json") }
func BenchmarkJSONPretty(b *testing.B) { benchRun(b, "--json", "--pretty") }
func BenchmarkSize(b *testing.B)       { benchRun(b, "--size", "-L", "1") }
func BenchmarkEntry(b *testing.B)      { benchRun(b, "--entry") }
func BenchmarkAI(b *testing.B)         { benchRun(b, "--ai") }

// BenchmarkTreeMemory reports what a built tree costs to keep in memory,
// per node, and how many garbage collections building it triggered:
//
//	SPROUT_BENCH_DIR=/path/to/llvm-project go test -run '^$' -bench TreeMemory -benchtime 3x
func BenchmarkTreeMemory(b *testing.B) {
	dir := os.Getenv("SPROUT_BENCH_DIR")
	if dir == "" {
		b.Skip("set SPROUT_BENCH_DIR to a large repository")
	}
	for _, c := range []struct {
		name string
		opts Options
	}{
		{"plain", Options{MaxDepth: -1}},
		{"stat", Options{MaxDepth: -1, Stat: true}},
	} {
		b.Run(c.name, func(b *testing.B) {
			var before, built, after runtime.MemStats
			for i := 0; i < b.N; i++ {
				runtime.GC()
				runtime.ReadMemStats(&before)
				t, err := BuildTree(dir, c.opts)
				if err != nil {
					b.Fatal(err)
				}
				runtime.ReadMemStats(&built)
				runtime.GC()
				runtime.ReadMemStats(&after)
				dirs, files := t.Root.Count()
				nodes := float64(dirs + files + 1)
				b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/nodes, "live-B/node")
				b.ReportMetric(float64(after.HeapObjects-before.HeapObjects)/nodes, "live-objs/node")
				b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/(1<<20), "live-MB")
				b.ReportMetric(float64(built.NumGC-before.NumGC), "gc/op")
				runtime.KeepAlive(t)
			}
		})
	}
}

// BenchmarkGraph times building the dependency graph alone (the tree is
// built once, outside the timer), without and with test files.
func BenchmarkGraph(b *testing.B) {
	dir := os.Getenv("SPROUT_BENCH_DIR")
	if dir == "" {
		b.Skip("set SPROUT_BENCH_DIR to a large repository")
	}
	t, err := BuildTree(dir, Options{MaxDepth: -1, ShowHidden: true, Stat: true})
	if err != nil {
		b.Fatal(err)
	}
	for _, tests := range []bool{false, true} {
		name := "no-tests"
		if tests {
			name = "with-tests"
		}
		b.Run(name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				g := buildGraph(dir, t, tests, true)
				b.ReportMetric(float64(len(g.Files)), "files")
				b.ReportMetric(float64(len(g.fwd)), "edges")
			}
		})
	}
}
