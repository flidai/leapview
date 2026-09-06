package planir

import "fmt"

// nextSourceAlias preserves the historical ordinal while skipping semantic
// dataset aliases that happen to use the generated rN namespace.
func nextSourceAlias(ctx sourceContext) string {
	count := 0
	used := map[string]bool{}
	for _, aliases := range ctx.aliases {
		count += len(aliases)
		for _, alias := range aliases {
			if alias != "" {
				used[alias] = true
			}
		}
	}
	for candidate := count + 1; ; candidate++ {
		alias := fmt.Sprintf("r%d", candidate)
		if !used[alias] {
			return alias
		}
	}
}
