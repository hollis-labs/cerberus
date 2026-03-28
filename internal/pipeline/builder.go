package pipeline

import (
	"fmt"

	"github.com/chrispian/cerberus/internal/domain"
)

// Builder provides a fluent API for constructing pipelines.
type Builder struct {
	id          string
	name        string
	description string
	stages      []*Stage
	err         error
}

// New creates a pipeline builder with the given ID.
func New(id string) *Builder {
	return &Builder{id: id, name: id}
}

// Name sets the pipeline's display name.
func (b *Builder) Name(name string) *Builder {
	b.name = name
	return b
}

// Description sets the pipeline's description.
func (b *Builder) Description(desc string) *Builder {
	b.description = desc
	return b
}

// Stage starts building a new stage and returns a StageBuilder.
func (b *Builder) Stage(name string) *StageBuilder {
	return &StageBuilder{
		parent: b,
		stage:  &Stage{Name: name},
	}
}

// Build validates and returns the pipeline.
func (b *Builder) Build() (*Pipeline, error) {
	if b.err != nil {
		return nil, b.err
	}
	if len(b.stages) == 0 {
		return nil, fmt.Errorf("pipeline %q has no stages", b.id)
	}

	// Validate stage names are unique and deps reference real stages
	names := make(map[string]bool, len(b.stages))
	for _, s := range b.stages {
		if names[s.Name] {
			return nil, fmt.Errorf("duplicate stage name %q in pipeline %q", s.Name, b.id)
		}
		names[s.Name] = true
	}
	for _, s := range b.stages {
		for _, dep := range s.DependsOn {
			if !names[dep] {
				return nil, fmt.Errorf("stage %q depends on unknown stage %q", s.Name, dep)
			}
		}
	}

	return &Pipeline{
		ID:          b.id,
		Name:        b.name,
		Description: b.description,
		Stages:      b.stages,
	}, nil
}

// StageBuilder builds a single stage within a pipeline.
type StageBuilder struct {
	parent *Builder
	stage  *Stage
}

// Action adds an action to this stage.
func (sb *StageBuilder) Action(a domain.Action) *StageBuilder {
	sb.stage.Actions = append(sb.stage.Actions, a)
	return sb
}

// DependsOn declares that this stage depends on the named stages completing first.
func (sb *StageBuilder) DependsOn(stages ...string) *StageBuilder {
	sb.stage.DependsOn = append(sb.stage.DependsOn, stages...)
	return sb
}

// Done finishes this stage and returns the parent Builder.
func (sb *StageBuilder) Done() *Builder {
	if len(sb.stage.Actions) == 0 {
		sb.parent.err = fmt.Errorf("stage %q has no actions", sb.stage.Name)
	}
	sb.parent.stages = append(sb.parent.stages, sb.stage)
	return sb.parent
}
