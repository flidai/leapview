//go:build fai518qualification

package hostinstall

// QualificationStage exercises the same immutable host-install staging
// primitive used by the production installer. It is intentionally compiled
// only for the bounded FAI-518 qualification lane; normal binaries cannot use
// this bridge as an alternate activation path.
func QualificationStage(paths Paths, image string, payload map[string][]byte) (string, error) {
	return stageGeneration(paths, image, payload)
}

// QualificationActivate exercises the production host-install activation
// primitive and its canonical payload links.
func QualificationActivate(paths Paths, generation string) error {
	if err := ensurePayloadLinks(paths); err != nil {
		return err
	}
	return activateGeneration(paths, generation)
}

// QualificationActiveGeneration reads the durable host-install activation
// pointer through the production validation helper.
func QualificationActiveGeneration(paths Paths) (string, error) {
	return activeGeneration(paths)
}
