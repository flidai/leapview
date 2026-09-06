package compiler

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"gopkg.in/yaml.v3"
)

type resourceEnvelope struct {
	APIVersion string                   `yaml:"apiVersion"`
	Kind       string                   `yaml:"kind"`
	Metadata   metadata                 `yaml:"metadata"`
	AIContext  *semanticmodel.AIContext `yaml:"aiContext"`
	Spec       yaml.Node                `yaml:"spec"`
}

type metadata struct {
	ID            string   `yaml:"id"`
	Name          string   `yaml:"name"`
	Title         string   `yaml:"title"`
	DisplayName   string   `yaml:"displayName"`
	Description   string   `yaml:"description"`
	Owner         string   `yaml:"owner"`
	Tags          []string `yaml:"tags"`
	Domain        string   `yaml:"domain"`
	Documentation string   `yaml:"documentation"`
	Provenance    struct {
		Origin string `yaml:"origin"`
		Path   string `yaml:"path"`
		Source string `yaml:"source"`
	} `yaml:"provenance"`
}
