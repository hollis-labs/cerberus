package config

import (
	"reflect"
	"testing"
)

func TestDiffConfigs(t *testing.T) {
	svcA := ServiceDef{ID: "a", Name: "A", Command: []string{"run"}}
	svcB := ServiceDef{ID: "b", Name: "B", Command: []string{"run"}}
	svcC := ServiceDef{ID: "c", Name: "C", Command: []string{"run"}}
	svcAChanged := ServiceDef{ID: "a", Name: "A", Command: []string{"run", "-v"}}

	tests := []struct {
		name     string
		old, new *Config
		want     Diff
	}{
		{
			name: "nil old treats all as added",
			old:  nil,
			new:  &Config{Services: []ServiceDef{svcA, svcB}},
			want: Diff{Added: []string{"a", "b"}},
		},
		{
			name: "nil new treats all as removed",
			old:  &Config{Services: []ServiceDef{svcA}},
			new:  nil,
			want: Diff{Removed: []string{"a"}},
		},
		{
			name: "no change",
			old:  &Config{Services: []ServiceDef{svcA, svcB}},
			new:  &Config{Services: []ServiceDef{svcA, svcB}},
			want: Diff{},
		},
		{
			name: "single added",
			old:  &Config{Services: []ServiceDef{svcA}},
			new:  &Config{Services: []ServiceDef{svcA, svcB}},
			want: Diff{Added: []string{"b"}},
		},
		{
			name: "single removed",
			old:  &Config{Services: []ServiceDef{svcA, svcB}},
			new:  &Config{Services: []ServiceDef{svcA}},
			want: Diff{Removed: []string{"b"}},
		},
		{
			name: "single changed (command)",
			old:  &Config{Services: []ServiceDef{svcA}},
			new:  &Config{Services: []ServiceDef{svcAChanged}},
			want: Diff{Changed: []string{"a"}},
		},
		{
			name: "mixed",
			old:  &Config{Services: []ServiceDef{svcA, svcB}},
			new:  &Config{Services: []ServiceDef{svcAChanged, svcC}},
			want: Diff{Added: []string{"c"}, Removed: []string{"b"}, Changed: []string{"a"}},
		},
		{
			name: "slugs sorted in output",
			old:  &Config{Services: []ServiceDef{}},
			new: &Config{Services: []ServiceDef{
				{ID: "z"}, {ID: "a"}, {ID: "m"},
			}},
			want: Diff{Added: []string{"a", "m", "z"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DiffConfigs(tt.old, tt.new)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("diff mismatch:\n got=%+v\nwant=%+v", got, tt.want)
			}
		})
	}
}

func TestDiffIgnoresEmptyIDs(t *testing.T) {
	// ServiceDefs with empty IDs shouldn't appear in any diff bucket.
	oldCfg := &Config{Services: []ServiceDef{{ID: ""}, {ID: "a"}}}
	newCfg := &Config{Services: []ServiceDef{{ID: "a"}, {ID: ""}}}
	got := DiffConfigs(oldCfg, newCfg)
	if !got.IsEmpty() {
		t.Fatalf("expected empty diff, got %+v", got)
	}
}

func TestDiffIsEmpty(t *testing.T) {
	if !(Diff{}).IsEmpty() {
		t.Error("zero Diff should be empty")
	}
	if (Diff{Added: []string{"a"}}).IsEmpty() {
		t.Error("Diff with Added should not be empty")
	}
}
