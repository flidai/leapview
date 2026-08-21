package servergo

import "github.com/Yacobolo/toolbelt/apigen/ir"

// emit is the stable orchestration seam retained by the public Emit entry
// point. It deliberately contains no formatting or endpoint traversal: those
// responsibilities belong to discoverEmissionPlan and render respectively.
func emit(doc ir.Document, opts Options) ([]byte, error) {
	plan, err := discoverEmissionPlan(doc, opts)
	if err != nil {
		return nil, err
	}
	return render(doc, opts, plan)
}
