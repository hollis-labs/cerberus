package hosts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// toMap renders v as the generic map its YAML form decodes to.
func toMap(v any) (map[string]any, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// normalize turns JSON numbers into ints where they are whole, so a patch
// from JSON encodes as 16, never 1.6e+01.
func normalize(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normalize(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalize(x)
		}
		return out
	}
	return v
}

// mergePatch applies an RFC 7386 JSON merge patch: objects merge, null
// deletes a key, anything else (including a list) replaces.
func mergePatch(dst map[string]any, patch map[string]any) {
	for k, pv := range patch {
		if pv == nil {
			delete(dst, k)
			continue
		}
		if pm, ok := pv.(map[string]any); ok {
			dm, _ := dst[k].(map[string]any)
			if dm == nil {
				dm = map[string]any{}
			}
			mergePatch(dm, pm)
			dst[k] = dm
			continue
		}
		dst[k] = pv
	}
}

// strictDecode decodes a generic map into out, refusing unknown keys.
func strictDecode(m map[string]any, out any) error {
	b, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(out)
}

// PatchHost applies a merge patch to h. The name cannot change.
func PatchHost(h Host, patch map[string]any) (Host, error) {
	if v, ok := patch["name"]; ok && v != h.Name {
		return h, fmt.Errorf("a host cannot be renamed (name %q); create the new host and remove the old one", h.Name)
	}
	m, err := toMap(h)
	if err != nil {
		return h, err
	}
	mergePatch(m, normalize(patch).(map[string]any))
	var out Host
	if err := strictDecode(m, &out); err != nil {
		return h, fmt.Errorf("the change is not a valid host record: %w", err)
	}
	return out, nil
}

// PatchComponent applies a merge patch to c. The id cannot change, and the
// location moves only through MoveComponent.
func PatchComponent(c Component, patch map[string]any) (Component, error) {
	if v, ok := patch["id"]; ok && v != c.ID {
		return c, fmt.Errorf("a component id cannot change (id %q)", c.ID)
	}
	if _, ok := patch["location"]; ok {
		return c, fmt.Errorf("a component's location changes with a move, not an update")
	}
	m, err := toMap(c)
	if err != nil {
		return c, err
	}
	mergePatch(m, normalize(patch).(map[string]any))
	var out Component
	if err := strictDecode(m, &out); err != nil {
		return c, fmt.Errorf("the change is not a valid component: %w", err)
	}
	return out, nil
}

// DecodeComponent reads a whole component from a generic map, refusing
// unknown keys.
func DecodeComponent(m map[string]any) (Component, error) {
	var c Component
	if err := strictDecode(normalize(m).(map[string]any), &c); err != nil {
		return c, fmt.Errorf("not a valid component: %w", err)
	}
	return c, nil
}

// LooksLikeSecret reports a string value that the redaction rules would
// mask: credential material has no place in a hosts record.
func LooksLikeSecret(s string) bool { return redact.Text(s) != s }

// FindSecret returns the dotted path of the first string in v that looks like
// a credential, or "".
func FindSecret(path string, v any) string {
	switch t := v.(type) {
	case string:
		if LooksLikeSecret(t) {
			return path
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if p := FindSecret(strings.TrimPrefix(path+"."+k, "."), t[k]); p != "" {
				return p
			}
		}
	case []any:
		for i, x := range t {
			if p := FindSecret(fmt.Sprintf("%s[%d]", path, i), x); p != "" {
				return p
			}
		}
	}
	return ""
}

// ---- file edits ----

func (f *File) hostIndex(name string) int {
	for i := range f.Hosts {
		if f.Hosts[i].Name == name {
			return i
		}
	}
	return -1
}

func (f *File) componentIndex(id string) int {
	for i := range f.Components {
		if f.Components[i].ID == id {
			return i
		}
	}
	return -1
}

// HostIndex and ComponentIndex find a record; -1 when absent.
func (f *File) HostIndex(name string) int    { return f.hostIndex(name) }
func (f *File) ComponentIndex(id string) int { return f.componentIndex(id) }

// slotOccupantRef returns a pointer to the occupant_component of the storage
// or PCIe slot named slot on host h, or nil when there is no such slot.
func slotOccupantRef(h *Host, slot string) *string {
	if h == nil || h.Hardware == nil {
		return nil
	}
	for i := range h.Hardware.StorageSlots {
		if h.Hardware.StorageSlots[i].ID == slot {
			return &h.Hardware.StorageSlots[i].OccupantComponent
		}
	}
	if c := h.Hardware.Ceilings; c != nil {
		for i := range c.PCIeSlots {
			if c.PCIeSlots[i].ID == slot {
				return &c.PCIeSlots[i].OccupantComponent
			}
		}
	}
	return nil
}

// vacate clears the slot side of component id's current location.
func (f *File) vacate(c *Component) {
	if c.Location.Spare || c.Location.Host == "" {
		return
	}
	if i := f.hostIndex(c.Location.Host); i >= 0 {
		if ref := slotOccupantRef(&f.Hosts[i], c.Location.Slot); ref != nil && *ref == c.ID {
			*ref = ""
		}
	}
}

// occupy sets the slot side for c's (new) location, refusing a slot that is
// already taken. Slots that carry no occupant fields (memory, cpu, free text)
// need nothing.
func (f *File) occupy(c *Component) error {
	if c.Location.Spare || c.Location.Host == "" {
		return nil
	}
	i := f.hostIndex(c.Location.Host)
	if i < 0 {
		return fmt.Errorf("host %q is not declared", c.Location.Host)
	}
	h := &f.Hosts[i]
	ref := slotOccupantRef(h, c.Location.Slot)
	if ref == nil {
		return nil
	}
	if *ref != "" && *ref != c.ID {
		return fmt.Errorf("slot %q of host %q already holds component %q", c.Location.Slot, h.Name, *ref)
	}
	for _, s := range h.Hardware.StorageSlots {
		if s.ID == c.Location.Slot && s.OccupantDisk != "" {
			return fmt.Errorf("slot %q of host %q already holds disk %q", c.Location.Slot, h.Name, s.OccupantDisk)
		}
	}
	*ref = c.ID
	return nil
}

// AddComponent appends c and fills the slot side of its location.
func (f *File) AddComponent(c Component) error {
	if f.componentIndex(c.ID) >= 0 {
		return fmt.Errorf("component %q already exists", c.ID)
	}
	if err := f.occupy(&c); err != nil {
		return err
	}
	f.Components = append(f.Components, c)
	return nil
}

// MoveComponent moves a component to a slot on a host or to the spares,
// keeping both slot sides in step.
func (f *File) MoveComponent(id string, to ComponentLocation) error {
	i := f.componentIndex(id)
	if i < 0 {
		return fmt.Errorf("component %q is not declared", id)
	}
	c := f.Components[i]
	f.vacate(&c)
	c.Location = to
	if err := f.occupy(&c); err != nil {
		return err
	}
	f.Components[i] = c
	return nil
}

// RemoveComponent deletes a component and clears the slot it held.
func (f *File) RemoveComponent(id string) error {
	i := f.componentIndex(id)
	if i < 0 {
		return fmt.Errorf("component %q is not declared", id)
	}
	c := f.Components[i]
	f.vacate(&c)
	f.Components = slices.Delete(f.Components, i, i+1)
	return nil
}

// ComponentsOn lists the ids of components located on host name.
func (f *File) ComponentsOn(name string) []string {
	var ids []string
	for _, c := range f.Components {
		if c.Location.Host == name {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// RemoveHost deletes a host that nothing references.
func (f *File) RemoveHost(name string) error {
	i := f.hostIndex(name)
	if i < 0 {
		return fmt.Errorf("host %q is not declared", name)
	}
	if ids := f.ComponentsOn(name); len(ids) > 0 {
		return fmt.Errorf("host %q still holds components (%s); move them to another host or to the spares, or remove them, first", name, strings.Join(ids, ", "))
	}
	f.Hosts = slices.Delete(f.Hosts, i, i+1)
	return nil
}
