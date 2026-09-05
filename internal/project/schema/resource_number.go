package configschema

import "strings"

// jsonFloatToken converts the YAML-only spellings accepted by yaml.v3 into a
// valid JSON number without routing the token through float64. The generated
// semantic access decoders can therefore retain exact decimal precision.
func jsonFloatToken(value string) string {
	canonical := strings.ReplaceAll(strings.TrimSpace(value), "_", "")
	canonical = strings.TrimPrefix(canonical, "+")
	exponent := ""
	if index := strings.IndexAny(canonical, "eE"); index >= 0 {
		exponent = canonical[index:]
		canonical = canonical[:index]
	}
	switch {
	case strings.HasPrefix(canonical, "."):
		canonical = "0" + canonical
	case strings.HasPrefix(canonical, "-."):
		canonical = "-0" + canonical[1:]
	}
	if strings.HasSuffix(canonical, ".") {
		canonical += "0"
	}
	return canonical + exponent
}
