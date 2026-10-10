package main

import (
	"fmt"
	"path/filepath"
)

// gradeDesignSource grades canonical output from an experimental lowerer. It does
// not grade prototype acceptance, authoring scope, provenance or process success;
// those independent gates belong to the experiment controller.
func gradeDesignSource(manifestPath, taskID, source string) (result, error) {
	r := result{ID: taskID}
	manifest, _, err := loadCorpusManifest(manifestPath)
	if err != nil {
		return r, err
	}
	base := filepath.Dir(manifestPath)
	if _, err := verifyCorpusInputs(base, manifest); err != nil {
		return r, err
	}
	var task *corpusTask
	for i := range manifest.Tasks {
		if manifest.Tasks[i].ID == taskID {
			task = &manifest.Tasks[i]
			break
		}
	}
	if task == nil {
		return r, fmt.Errorf("unknown corpus task %q", taskID)
	}
	r.Files, r.SourceHash, r.SourceBytes, err = sourceFiles(source)
	if err != nil {
		return r, err
	}
	actual, err := loadCorpusDocument(source)
	if err != nil {
		r.Diagnostic = err.Error()
		return r, nil
	}
	r.ParseAndSchema = true
	if err := compileCorpusRoot(filepath.Join(base, manifest.SupportRoot), source); err != nil {
		r.Diagnostic = err.Error()
		return r, nil
	}
	r.Compiler = true
	expected, err := loadCorpusDocument(filepath.Join(base, task.OracleRoot))
	if err != nil {
		return r, err
	}
	r.Intent, err = corpusIntentEqual(actual, expected)
	if err != nil {
		return r, err
	}
	r.Pass = r.Intent
	if !r.Intent {
		r.Diagnostic = "Compiled document differs from the frozen authored-behavior oracle"
	}
	return r, nil
}
