package hosts

import (
	"fmt"
	"slices"
	"strings"
)

// Sources of a ceiling value. DMI understates what a board takes, so every
// limit says where it came from; Cerberus shows the source and never picks
// between them.
const (
	SourceDMI        = "dmi"
	SourceVendorSpec = "vendor_spec"
	SourceOperator   = "operator"
)

// Sources are the recognized ceiling sources.
var Sources = []string{SourceDMI, SourceVendorSpec, SourceOperator}

// Sourced is a recorded value and where it came from.
type Sourced[T any] struct {
	Value  T      `yaml:"value" json:"value"`
	Source string `yaml:"source" json:"source"`
}

// SourcedText is a recorded string that came off the machine (a socket name),
// so it is marked untrusted on every surface.
type SourcedText struct {
	Value  string `yaml:"value" json:"value" cerb:"untrusted"`
	Source string `yaml:"source" json:"source"`
}

// SourcedTexts is a recorded list of strings that came off the machine (DMI
// slot locators), marked untrusted.
type SourcedTexts struct {
	Value  []string `yaml:"value" json:"value" cerb:"untrusted"`
	Source string   `yaml:"source" json:"source"`
}

// Ceilings is what a machine can take: the upgrade limits.
type Ceilings struct {
	CPU       *CPUCeiling `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	Memory    *MemCeiling `yaml:"memory,omitempty" json:"memory,omitempty"`
	PCIeSlots []PCIeSlot  `yaml:"pcie_slots,omitempty" json:"pcie_slots,omitempty"`
	GPU       *GPUCeiling `yaml:"gpu,omitempty" json:"gpu,omitempty"`
}

// CPUCeiling is the socket and what it can hold.
type CPUCeiling struct {
	Socket       *SourcedText `yaml:"socket,omitempty" json:"socket,omitempty"`
	Installed    *CPUSpec     `yaml:"installed,omitempty" json:"installed,omitempty"`
	MaxSupported *CPUSpec     `yaml:"max_supported,omitempty" json:"max_supported,omitempty"`
}

// CPUSpec is a processor's size. Source says where the numbers came from.
type CPUSpec struct {
	Cores   int    `yaml:"cores,omitempty" json:"cores,omitempty"`
	Threads int    `yaml:"threads,omitempty" json:"threads,omitempty"`
	TDPW    int    `yaml:"tdp_w,omitempty" json:"tdp_w,omitempty"`
	Source  string `yaml:"source" json:"source"`
}

// MemCeiling is the memory limit.
type MemCeiling struct {
	Slots      *Sourced[int]      `yaml:"slots,omitempty" json:"slots,omitempty"`
	MaxTotal   *Sourced[Size]     `yaml:"max_total,omitempty" json:"max_total,omitempty"`
	MaxPerSlot *Sourced[Size]     `yaml:"max_per_slot,omitempty" json:"max_per_slot,omitempty"`
	Types      *Sourced[[]string] `yaml:"types,omitempty" json:"types,omitempty"`
	ECC        *Sourced[bool]     `yaml:"ecc,omitempty" json:"ecc,omitempty"`
	// SlotIDs names the memory slots for boards whose DMI locators are named
	// (for example ChannelA-DIMM0). Default dimm0..N-1.
	SlotIDs *SourcedTexts `yaml:"slot_ids,omitempty" json:"slot_ids,omitempty"`
}

// PCIeSlot is one expansion slot for any card: GPU, HBA, NIC, capture.
type PCIeSlot struct {
	ID                string `yaml:"id" json:"id"`
	Gen               int    `yaml:"gen,omitempty" json:"gen,omitempty"`
	Lanes             int    `yaml:"lanes,omitempty" json:"lanes,omitempty"`
	Form              string `yaml:"form,omitempty" json:"form,omitempty"`
	MaxPowerW         int    `yaml:"max_power_w,omitempty" json:"max_power_w,omitempty"`
	OccupantComponent string `yaml:"occupant_component,omitempty" json:"occupant_component,omitempty"`
	Source            string `yaml:"source" json:"source"`
}

// GPUCeiling holds GPU-specific limits only.
type GPUCeiling struct {
	PowerBudgetW *Sourced[int] `yaml:"power_budget_w,omitempty" json:"power_budget_w,omitempty"`
}

// Slot kinds.
var StorageSlotKinds = []string{"nvme_m2", "sata_25", "sata_35", "m2_sata", "ide", "pcie"}

// SlotInterfaces are the recognized slot interfaces.
var SlotInterfaces = []string{"pcie", "sata", "ide", "usb"}

// StorageSlot is one physical place a drive can go.
type StorageSlot struct {
	ID         string         `yaml:"id" json:"id"`
	Kind       string         `yaml:"kind" json:"kind"`
	FormFactor string         `yaml:"form_factor,omitempty" json:"form_factor,omitempty"`
	Interface  string         `yaml:"interface,omitempty" json:"interface,omitempty"`
	Gen        int            `yaml:"gen,omitempty" json:"gen,omitempty"`
	MaxSize    *Sourced[Size] `yaml:"max_size,omitempty" json:"max_size,omitempty"`
	// At most one occupant is set; neither means the slot is free.
	OccupantDisk      string `yaml:"occupant_disk,omitempty" json:"occupant_disk,omitempty"`
	OccupantComponent string `yaml:"occupant_component,omitempty" json:"occupant_component,omitempty"`
	Notes             string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Free reports whether nothing occupies the slot.
func (s StorageSlot) Free() bool { return s.OccupantDisk == "" && s.OccupantComponent == "" }

// SlotCount is used and free slots of one kind.
type SlotCount struct {
	Total int `json:"total"`
	Used  int `json:"used"`
	Free  int `json:"free"`
}

// StorageSummary counts slots per kind. A host with no recorded slots has no
// summary (nil): unknown is not zero.
func StorageSummary(slots []StorageSlot) map[string]SlotCount {
	if slots == nil {
		return nil
	}
	out := map[string]SlotCount{}
	for _, s := range slots {
		c := out[s.Kind]
		c.Total++
		if s.Free() {
			c.Free++
		} else {
			c.Used++
		}
		out[s.Kind] = c
	}
	return out
}

// Component kinds.
var ComponentKinds = []string{"ram", "disk", "cpu", "other"}

// Component is a part, installed or spare.
type Component struct {
	ID          string            `yaml:"id" json:"id"`
	Kind        string            `yaml:"kind" json:"kind"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty" cerb:"untrusted"`
	Size        Size              `yaml:"size,omitempty" json:"size,omitempty"`
	Type        string            `yaml:"type,omitempty" json:"type,omitempty" cerb:"untrusted"`
	Speed       string            `yaml:"speed,omitempty" json:"speed,omitempty" cerb:"untrusted"`
	Serial      string            `yaml:"serial,omitempty" json:"serial,omitempty" cerb:"untrusted"`
	Location    ComponentLocation `yaml:"location" json:"location"`
	Notes       string            `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// ComponentLocation is a host and slot, or the spare drawer.
type ComponentLocation struct {
	Host  string `yaml:"host,omitempty" json:"host,omitempty"`
	Slot  string `yaml:"slot,omitempty" json:"slot,omitempty"`
	Spare bool   `yaml:"spare,omitempty" json:"spare,omitempty"`
}

const (
	maxComponents = 256
	maxSlotText   = 64
)

// MemorySlotIDs are the slot names a RAM component may use on h:
// ceilings.memory.slot_ids, else dimm0..N-1 with N from ceilings.memory.slots,
// else hardware.memory.slots. Nil when the host records none (unknown).
func MemorySlotIDs(h Host) []string {
	if h.Hardware == nil {
		return nil
	}
	if c := h.Hardware.Ceilings; c != nil && c.Memory != nil {
		if ids := c.Memory.SlotIDs; ids != nil && len(ids.Value) > 0 {
			return ids.Value
		}
		if c.Memory.Slots != nil && c.Memory.Slots.Value > 0 {
			return dimmIDs(c.Memory.Slots.Value)
		}
	}
	if m := h.Hardware.Memory; m != nil && m.Slots > 0 {
		return dimmIDs(m.Slots)
	}
	return nil
}

func dimmIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("dimm%d", i)
	}
	return ids
}

// validateParts checks ceilings, storage slots and components, including the
// cross-checks between hosts, slots, disks and components. Field paths name
// the host (hosts) or the component id.
func validateParts(f *File, add func(host, field, format string, args ...any)) {
	hostsByName := map[string]*Host{}
	for i := range f.Hosts {
		hostsByName[f.Hosts[i].Name] = &f.Hosts[i]
	}

	compByID := map[string]*Component{}
	if len(f.Components) > maxComponents {
		add("", "components", "at most %d", maxComponents)
	}
	for i := range f.Components {
		c := &f.Components[i]
		at := fmt.Sprintf("components[%d]", i)
		if c.ID == "" || !nameRE.MatchString(c.ID) {
			add("", at+".id", "%q must be lowercase letters, digits, '.', '_' or '-', at most 63 characters", c.ID)
		} else if _, dup := compByID[c.ID]; dup {
			add("", at+".id", "duplicate component id %q", c.ID)
		} else {
			compByID[c.ID] = c
		}
	}

	for i := range f.Hosts {
		h := &f.Hosts[i]
		if h.Hardware == nil {
			continue
		}
		validateCeilings(h, func(field, msg string) { add(h.Name, field, "%s", msg) })
		validateStorageSlots(h, compByID, func(field, msg string) { add(h.Name, field, "%s", msg) })
	}

	// Which component sits where, for the one-per-slot rule.
	occupied := map[string]string{}
	for i := range f.Components {
		c := &f.Components[i]
		at := fmt.Sprintf("components[%d]", i)
		text := func(field, v string) {
			if len(v) > maxHardwareText {
				add("", at+"."+field, "longer than %d characters", maxHardwareText)
				return
			}
			for _, r := range v {
				if r < 0x20 || r == 0x7f {
					add("", at+"."+field, "must not contain control characters")
					return
				}
			}
		}
		text("description", c.Description)
		text("type", c.Type)
		text("speed", c.Speed)
		text("serial", c.Serial)
		if len(c.Notes) > 1024 {
			add("", at+".notes", "longer than 1024 characters")
		}
		if !slices.Contains(ComponentKinds, c.Kind) {
			add("", at+".kind", "unknown kind %q; known: %s", c.Kind, strings.Join(ComponentKinds, ", "))
		}
		if c.Size < 0 {
			add("", at+".size", "must not be negative")
		}
		loc := c.Location
		switch {
		case loc.Spare && (loc.Host != "" || loc.Slot != ""):
			add("", at+".location", "a spare part has no host or slot")
		case loc.Spare:
		case loc.Host == "" || loc.Slot == "":
			add("", at+".location", "set host and slot, or spare: true")
		default:
			h, ok := hostsByName[loc.Host]
			if !ok {
				add("", at+".location.host", "host %q is not declared", loc.Host)
				break
			}
			if len(loc.Slot) > maxSlotText {
				add("", at+".location.slot", "longer than %d characters", maxSlotText)
				break
			}
			key := loc.Host + "/" + loc.Slot
			if other, taken := occupied[key]; taken {
				add("", at+".location.slot", "slot %s is also taken by component %q", key, other)
			} else {
				occupied[key] = c.ID
			}
			validateComponentSlot(c, h, func(field, msg string) { add("", at+"."+field, "%s", msg) })
		}
	}
}

func validateCeilings(h *Host, add func(field, msg string)) {
	cl := h.Hardware.Ceilings
	if cl == nil {
		return
	}
	src := func(field, s string) {
		if !slices.Contains(Sources, s) {
			add(field+".source", fmt.Sprintf("must be one of %s (got %q)", strings.Join(Sources, ", "), s))
		}
	}
	if c := cl.CPU; c != nil {
		if c.Socket != nil {
			src("hardware.ceilings.cpu.socket", c.Socket.Source)
			if len(c.Socket.Value) > maxHardwareText {
				add("hardware.ceilings.cpu.socket.value", "too long")
			}
		}
		for name, s := range map[string]*CPUSpec{"installed": c.Installed, "max_supported": c.MaxSupported} {
			if s == nil {
				continue
			}
			at := "hardware.ceilings.cpu." + name
			src(at, s.Source)
			if s.Cores < 0 || s.Threads < 0 || s.TDPW < 0 {
				add(at, "cores, threads and tdp_w must not be negative")
			}
		}
		if i, m := c.Installed, c.MaxSupported; i != nil && m != nil &&
			((m.Cores > 0 && i.Cores > m.Cores) || (m.Threads > 0 && i.Threads > m.Threads) || (m.TDPW > 0 && i.TDPW > m.TDPW)) {
			add("hardware.ceilings.cpu.installed", "exceeds max_supported")
		}
	}
	if m := cl.Memory; m != nil {
		at := "hardware.ceilings.memory"
		if m.Slots != nil {
			src(at+".slots", m.Slots.Source)
			if m.Slots.Value < 0 {
				add(at+".slots", "must not be negative")
			}
			if hm := h.Hardware.Memory; hm != nil && hm.Slots > 0 && m.Slots.Value > 0 && hm.Slots != m.Slots.Value {
				add(at+".slots", fmt.Sprintf("%d disagrees with hardware.memory.slots %d", m.Slots.Value, hm.Slots))
			}
		}
		if m.MaxTotal != nil {
			src(at+".max_total", m.MaxTotal.Source)
			if m.MaxTotal.Value < 0 {
				add(at+".max_total", "must not be negative")
			}
		}
		if m.MaxPerSlot != nil {
			src(at+".max_per_slot", m.MaxPerSlot.Source)
			if m.MaxPerSlot.Value < 0 {
				add(at+".max_per_slot", "must not be negative")
			}
		}
		if m.Types != nil {
			src(at+".types", m.Types.Source)
		}
		if m.ECC != nil {
			src(at+".ecc", m.ECC.Source)
		}
		if m.SlotIDs != nil {
			src(at+".slot_ids", m.SlotIDs.Source)
			if len(m.SlotIDs.Value) > maxHardwareItems {
				add(at+".slot_ids", fmt.Sprintf("at most %d", maxHardwareItems))
			}
			seen := map[string]bool{}
			for i, id := range m.SlotIDs.Value {
				if id == "" || len(id) > maxSlotText || strings.ContainsAny(id, " \t\r\n/") {
					add(fmt.Sprintf("%s.slot_ids.value[%d]", at, i), fmt.Sprintf("%q is not a slot name", id))
				}
				if seen[id] {
					add(fmt.Sprintf("%s.slot_ids.value[%d]", at, i), fmt.Sprintf("duplicate slot %q", id))
				}
				seen[id] = true
			}
			if m.Slots != nil && m.Slots.Value > 0 && len(m.SlotIDs.Value) != m.Slots.Value {
				add(at+".slot_ids", fmt.Sprintf("%d names for %d slots", len(m.SlotIDs.Value), m.Slots.Value))
			}
		}
		if m.MaxTotal != nil && m.MaxPerSlot != nil && m.Slots != nil && m.Slots.Value > 0 &&
			m.MaxPerSlot.Value > 0 && m.MaxTotal.Value > m.MaxPerSlot.Value*Size(m.Slots.Value) {
			add(at+".max_total", "is more than max_per_slot times slots")
		}
	}
	if g := cl.GPU; g != nil && g.PowerBudgetW != nil {
		src("hardware.ceilings.gpu.power_budget_w", g.PowerBudgetW.Source)
		if g.PowerBudgetW.Value < 0 {
			add("hardware.ceilings.gpu.power_budget_w", "must not be negative")
		}
	}
	if len(cl.PCIeSlots) > maxHardwareItems {
		add("hardware.ceilings.pcie_slots", fmt.Sprintf("at most %d", maxHardwareItems))
	}
	seen := map[string]bool{}
	for i, p := range cl.PCIeSlots {
		at := fmt.Sprintf("hardware.ceilings.pcie_slots[%d]", i)
		if p.ID == "" || !identRE.MatchString(p.ID) {
			add(at+".id", fmt.Sprintf("%q is not a slot id", p.ID))
		} else if seen[p.ID] {
			add(at+".id", fmt.Sprintf("duplicate slot %q", p.ID))
		}
		seen[p.ID] = true
		src(at, p.Source)
		if p.Gen < 0 || p.Lanes < 0 || p.MaxPowerW < 0 {
			add(at, "gen, lanes and max_power_w must not be negative")
		}
		if len(p.Form) > maxHardwareText {
			add(at+".form", "too long")
		}
	}
}

func validateStorageSlots(h *Host, comps map[string]*Component, add func(field, msg string)) {
	slots := h.Hardware.StorageSlots
	if len(slots) > maxHardwareItems {
		add("hardware.storage_slots", fmt.Sprintf("at most %d", maxHardwareItems))
	}
	disks := map[string]bool{}
	for _, d := range h.Hardware.Disks {
		if d.Name != "" {
			disks[d.Name] = true
		}
	}
	seen := map[string]bool{}
	diskUsed := map[string]string{}
	compUsed := map[string]string{}
	for i, s := range slots {
		at := fmt.Sprintf("hardware.storage_slots[%d]", i)
		if s.ID == "" || !identRE.MatchString(s.ID) {
			add(at+".id", fmt.Sprintf("%q is not a slot id (lowercase letters, digits, '.', '_', '-')", s.ID))
		} else if seen[s.ID] {
			add(at+".id", fmt.Sprintf("duplicate slot %q", s.ID))
		}
		seen[s.ID] = true
		if !slices.Contains(StorageSlotKinds, s.Kind) {
			add(at+".kind", fmt.Sprintf("unknown kind %q; known: %s", s.Kind, strings.Join(StorageSlotKinds, ", ")))
		}
		if s.Interface != "" && !slices.Contains(SlotInterfaces, s.Interface) {
			add(at+".interface", fmt.Sprintf("unknown interface %q; known: %s", s.Interface, strings.Join(SlotInterfaces, ", ")))
		}
		if s.Gen < 0 {
			add(at+".gen", "must not be negative")
		}
		for _, v := range []struct{ f, v string }{{"form_factor", s.FormFactor}} {
			if len(v.v) > maxHardwareText {
				add(at+"."+v.f, "too long")
			}
		}
		if len(s.Notes) > 1024 {
			add(at+".notes", "longer than 1024 characters")
		}
		if s.MaxSize != nil {
			if !slices.Contains(Sources, s.MaxSize.Source) {
				add(at+".max_size.source", fmt.Sprintf("must be one of %s (got %q)", strings.Join(Sources, ", "), s.MaxSize.Source))
			}
			if s.MaxSize.Value < 0 {
				add(at+".max_size", "must not be negative")
			}
		}
		if s.OccupantDisk != "" && s.OccupantComponent != "" {
			add(at, "set occupant_disk or occupant_component, not both")
		}
		if s.OccupantDisk != "" {
			if !disks[s.OccupantDisk] {
				add(at+".occupant_disk", fmt.Sprintf("%q is not in hardware.disks", s.OccupantDisk))
			} else if other, dup := diskUsed[s.OccupantDisk]; dup {
				add(at+".occupant_disk", fmt.Sprintf("disk %q is already in slot %q", s.OccupantDisk, other))
			}
			diskUsed[s.OccupantDisk] = s.ID
		}
		if s.OccupantComponent != "" {
			checkComponentOccupant(h, comps, s.ID, s.OccupantComponent, at+".occupant_component", compUsed, add)
		}
	}
	if cl := h.Hardware.Ceilings; cl != nil {
		for i, p := range cl.PCIeSlots {
			if p.OccupantComponent != "" {
				checkComponentOccupant(h, comps, p.ID, p.OccupantComponent,
					fmt.Sprintf("hardware.ceilings.pcie_slots[%d].occupant_component", i), compUsed, add)
			}
		}
	}
}

// checkComponentOccupant checks that a slot's occupant_component exists, is
// located on this host and slot, and is not in two slots.
func checkComponentOccupant(h *Host, comps map[string]*Component, slot, id, at string, used map[string]string, add func(field, msg string)) {
	c, ok := comps[id]
	switch {
	case !ok:
		add(at, fmt.Sprintf("component %q is not declared in components", id))
		return
	case c.Location.Host != h.Name || c.Location.Slot != slot:
		add(at, fmt.Sprintf("component %q is located at %s, not host %q slot %q", id, locationText(c.Location), h.Name, slot))
	}
	if other, dup := used[id]; dup {
		add(at, fmt.Sprintf("component %q is already in slot %q", id, other))
	}
	used[id] = slot
}

func locationText(l ComponentLocation) string {
	if l.Spare {
		return "the spare drawer"
	}
	return fmt.Sprintf("host %q slot %q", l.Host, l.Slot)
}

// validateComponentSlot checks a located component's slot against its host
// and that the slot side agrees.
func validateComponentSlot(c *Component, h *Host, add func(field, msg string)) {
	slot := c.Location.Slot
	switch c.Kind {
	case "ram":
		ids := MemorySlotIDs(*h)
		if ids == nil {
			add("location.slot", fmt.Sprintf("host %q records no memory slots; set hardware.memory.slots or ceilings.memory.slot_ids", h.Name))
		} else if !slices.Contains(ids, slot) {
			add("location.slot", fmt.Sprintf("%q is not a memory slot of host %q (%s)", slot, h.Name, strings.Join(ids, ", ")))
		}
	case "cpu":
		if slot != "socket" {
			add("location.slot", `a cpu goes in slot "socket"`)
		}
	case "disk":
		var found *StorageSlot
		if h.Hardware != nil {
			for i := range h.Hardware.StorageSlots {
				if h.Hardware.StorageSlots[i].ID == slot {
					found = &h.Hardware.StorageSlots[i]
				}
			}
		}
		switch {
		case found == nil:
			add("location.slot", fmt.Sprintf("%q is not in host %q hardware.storage_slots", slot, h.Name))
		case found.OccupantComponent != c.ID:
			add("location.slot", fmt.Sprintf("slot %q of host %q does not list this component as occupant_component", slot, h.Name))
		}
	default: // other: a PCIe slot must agree; anything else is free text
		if h.Hardware != nil && h.Hardware.Ceilings != nil {
			for _, p := range h.Hardware.Ceilings.PCIeSlots {
				if p.ID == slot && p.OccupantComponent != c.ID {
					add("location.slot", fmt.Sprintf("pcie slot %q of host %q does not list this component as occupant_component", slot, h.Name))
				}
			}
		}
	}
}

// SlotWarnings are findings that do not make the file invalid: a disk larger
// than its slot's recorded max_size (the ceiling may be wrong).
func SlotWarnings(h Host) []string {
	if h.Hardware == nil {
		return nil
	}
	var out []string
	sizes := map[string]Size{}
	for _, d := range h.Hardware.Disks {
		sizes[d.Name] = d.Size
	}
	for _, s := range h.Hardware.StorageSlots {
		if s.MaxSize == nil || s.MaxSize.Value <= 0 {
			continue
		}
		if sz := sizes[s.OccupantDisk]; s.OccupantDisk != "" && sz > s.MaxSize.Value {
			out = append(out, fmt.Sprintf("slot %s: disk %s is larger than the slot's recorded max_size (%s); the ceiling may be wrong", s.ID, s.OccupantDisk, s.MaxSize.Source))
		}
	}
	return out
}
