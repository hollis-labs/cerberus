package domain

import "time"

// Project groups related Resources. A project might contain a local dev server,
// a cloud VM, a DNS record, and a GitHub repo — all managed together.
type Project struct {
	ID          string    `json:"id" yaml:"id"`
	Name        string    `json:"name" yaml:"name"`
	Description string    `json:"description,omitempty" yaml:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at" yaml:"-"`
	UpdatedAt   time.Time `json:"updated_at" yaml:"-"`
}
