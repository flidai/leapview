package architecture

// ADR-0021 defines guided deploy as the reviewed production delivery step
// after local authoring, not as an alternate hosted authoring lifecycle.
func analyticsDevelopmentGuideAllows(name, command string) bool {
	return name == "analytics-development.md" && command == "leapview deploy"
}
