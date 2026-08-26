// Package qualificationbarrier defines the inert filesystem markers shared by
// the production activation boundary and the external qualification client.
package qualificationbarrier

const (
	// ArmedMarker asks an evaluation deployment to pause immediately before its
	// target activation compare-and-swap.
	ArmedMarker = ".qualification-activation-barrier.armed"

	// ReachedMarker proves that an armed evaluation deployment reached the
	// activation boundary and consumed ArmedMarker.
	ReachedMarker = ".qualification-activation-barrier.reached"
)
