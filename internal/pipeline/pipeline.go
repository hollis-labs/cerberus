package pipeline

import "github.com/chrispian/cerberus/internal/domain"

// Pipeline is a named sequence of stages, each containing actions.
// Stages form a DAG via DependsOn — stages at the same level run in parallel.
type Pipeline struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Stages      []*Stage `yaml:"stages" json:"stages"`
}

// Stage is a named step in a pipeline. It contains one or more actions
// and may depend on other stages completing first.
type Stage struct {
	Name      string          `yaml:"name" json:"name"`
	DependsOn []string        `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Actions   []domain.Action `yaml:"-" json:"-"`
}
