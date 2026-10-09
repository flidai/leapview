package module

import "github.com/flidai/leapview/internal/project"

// CandidateSourceScope identifies the project and explicit owner intent passed
// to the retained-source reader. The native delivery coordinator separately
// authorizes source ownership; constructing this value grants no access.
type CandidateSourceScope = project.CandidateSourceScope
