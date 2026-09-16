package codecontext

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	treesitter "github.com/tree-sitter/go-tree-sitter"
)

// Measures parsing and Tree cleanup, not full Extract or end-to-end review.
// ReportAllocs tracks Go allocations only; it does not measure C memory/RSS.
func BenchmarkParserLifecycle(b *testing.B) {
	// Retain the previous global-lock strategy as a measurable baseline.
	var globalMu sync.Mutex
	globalParsers := map[string]*treesitter.Parser{}
	b.Cleanup(func() {
		for _, parser := range globalParsers {
			parser.Close()
		}
	})

	for _, size := range []int{1, 100} {
		sources := []struct{ name, content string }{{"go", "package sample\n"}, {"python", ""}, {"javascript", ""}}
		for i := 0; i < size; i++ {
			sources[0].content += fmt.Sprintf("func Function%d(x int) int { return x+1 }\n", i)
			sources[1].content += fmt.Sprintf("def function%d(x):\n    return x+1\n", i)
			sources[2].content += fmt.Sprintf("function function%d(x) { return x+1; }\n", i)
		}
		for _, strategy := range []string{"fresh", "global_lock", "per_language"} {
			parse := func(name, content string) *treesitter.Tree {
				if strategy == "per_language" {
					return parseWithCachedParser(name, content)
				}
				if strategy == "global_lock" {
					globalMu.Lock()
					defer globalMu.Unlock()
					parser := globalParsers[name]
					if parser == nil {
						constructor, _ := parserLanguage(name)
						parser = treesitter.NewParser()
						if err := parser.SetLanguage(treesitter.NewLanguage(constructor())); err != nil {
							panic(err)
						}
						globalParsers[name] = parser
					}
					return parser.Parse([]byte(content), nil)
				}
				constructor, _ := parserLanguage(name)
				parser := treesitter.NewParser()
				defer parser.Close()
				if err := parser.SetLanguage(treesitter.NewLanguage(constructor())); err != nil {
					panic(err)
				}
				return parser.Parse([]byte(content), nil)
			}
			for _, parallel := range []bool{false, true} {
				mode := "serial"
				if parallel {
					mode = "parallel"
				}
				b.Run(fmt.Sprintf("functions=%d/%s/%s", size, strategy, mode), func(b *testing.B) {
					for _, source := range sources {
						tree := parse(source.name, source.content)
						if tree == nil {
							b.Fatal("parse failed")
						}
						tree.Close()
					}
					b.ReportAllocs()
					b.ResetTimer()
					run := func(index int) {
						source := sources[index%len(sources)]
						tree := parse(source.name, source.content)
						if tree == nil {
							panic("parse failed")
						}
						tree.Close()
					}
					if parallel {
						var workers atomic.Int64
						b.RunParallel(func(pb *testing.PB) {
							index := int(workers.Add(1))
							for pb.Next() {
								run(index)
								index++
							}
						})
					} else {
						for i := 0; i < b.N; i++ {
							run(i)
						}
					}
				})
			}
		}
	}
}
