package hosts

import (
	"strings"
	"testing"
)

const partsBase = `version: 1
hosts:
  - name: box-a
    role: worker
    hardware:
      memory: {type: DDR4, total: 8GiB, max: 32GiB, slots: 2, slots_used: 1}
      disks: [{name: sda, size: 120GB, type: ssd, bus: sata}, {name: nvme0n1, size: 1TB, type: nvme}]
`

func parseOK(t *testing.T, y string) *File {
	t.Helper()
	f, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

func parseErr(t *testing.T, y, want string) {
	t.Helper()
	_, err := Parse([]byte(y))
	if err == nil {
		t.Fatalf("want error containing %q, got none", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("want error containing %q, got:\n%v", want, err)
	}
}

func TestHardwareWithoutCeilingsIsUnknown(t *testing.T) {
	f := parseOK(t, partsBase)
	h := f.Hosts[0]
	if h.Hardware.Ceilings != nil || h.Hardware.StorageSlots != nil {
		t.Fatal("nothing recorded must stay nil")
	}
	if StorageSummary(h.Hardware.StorageSlots) != nil {
		t.Fatal("no slots recorded must have no summary, not zero counts")
	}
	if got := MemorySlotIDs(h); strings.Join(got, ",") != "dimm0,dimm1" {
		t.Fatalf("memory slot fallback to hardware.memory.slots: %v", got)
	}
}

func TestStorageSlotsAndSummary(t *testing.T) {
	f := parseOK(t, partsBase+`      storage_slots:
        - {id: m2-0, kind: nvme_m2, form_factor: "2280", interface: pcie, gen: 3, max_size: {value: 2TB, source: operator}, occupant_disk: nvme0n1}
        - {id: sata-0, kind: sata_25, interface: sata, gen: 3, occupant_disk: sda}
        - {id: sata-1, kind: sata_25, interface: sata, gen: 3}
`)
	sum := StorageSummary(f.Hosts[0].Hardware.StorageSlots)
	if sum["sata_25"] != (SlotCount{Total: 2, Used: 1, Free: 1}) || sum["nvme_m2"] != (SlotCount{Total: 1, Used: 1}) {
		t.Fatalf("summary: %+v", sum)
	}
}

func TestOccupantErrorsNameTheField(t *testing.T) {
	parseErr(t, partsBase+`      storage_slots:
        - {id: s0, kind: sata_25, occupant_disk: sdz}
`, `storage_slots[0].occupant_disk: "sdz" is not in hardware.disks`)
	parseErr(t, partsBase+`      storage_slots:
        - {id: s0, kind: sata_25, occupant_component: nope}
`, `storage_slots[0].occupant_component: component "nope" is not declared`)
	parseErr(t, partsBase+`      storage_slots:
        - {id: s0, kind: sata_25, occupant_disk: sda, occupant_component: x}
`, "not both")
	parseErr(t, partsBase+`      storage_slots:
        - {id: s0, kind: sata_25, occupant_disk: sda}
        - {id: s1, kind: sata_25, occupant_disk: sda}
`, `disk "sda" is already in slot "s0"`)
	parseErr(t, partsBase+`      storage_slots:
        - {id: s0, kind: floppy}
`, `unknown kind "floppy"`)
}

func TestComponentSlotAgreement(t *testing.T) {
	ok := partsBase + `      storage_slots:
        - {id: s0, kind: sata_25, occupant_component: d1}
components:
  - {id: d1, kind: disk, size: 1TB, type: hdd, serial: ABC, location: {host: box-a, slot: s0}}
  - {id: r1, kind: ram, size: 8GiB, type: DDR4, location: {host: box-a, slot: dimm1}}
  - {id: r2, kind: ram, size: 8GiB, location: {spare: true}}
`
	f := parseOK(t, ok)
	if len(f.Components) != 3 {
		t.Fatalf("components: %d", len(f.Components))
	}
	// Component says s0, slot lists nobody.
	parseErr(t, partsBase+`      storage_slots:
        - {id: s0, kind: sata_25}
components:
  - {id: d1, kind: disk, location: {host: box-a, slot: s0}}
`, "does not list this component as occupant_component")
	// Slot lists a component located elsewhere.
	parseErr(t, partsBase+`      storage_slots:
        - {id: s0, kind: sata_25, occupant_component: d1}
components:
  - {id: d1, kind: disk, location: {spare: true}}
`, `component "d1" is located at the spare drawer`)
	parseErr(t, ok+`  - {id: r3, kind: ram, location: {host: box-a, slot: dimm1}}
`, `is also taken by component "r1"`)
	parseErr(t, partsBase+`components:
  - {id: r1, kind: ram, location: {host: box-a, slot: dimm9}}
`, `"dimm9" is not a memory slot of host "box-a"`)
	parseErr(t, partsBase+`components:
  - {id: r1, kind: ram, location: {host: nope, slot: dimm0}}
`, `host "nope" is not declared`)
	parseErr(t, partsBase+`components:
  - {id: r1, kind: ram, location: {host: box-a, spare: true}}
`, "a spare part has no host or slot")
	parseErr(t, partsBase+`components:
  - {id: r1, kind: gizmo, location: {spare: true}}
  - {id: r1, kind: ram, location: {spare: true}}
`, "duplicate component id")
}

func TestMemorySlotIDsOrder(t *testing.T) {
	f := parseOK(t, partsBase+`      ceilings:
        memory:
          slots: {value: 2, source: dmi}
          max_total: {value: 32GiB, source: vendor_spec}
          max_per_slot: {value: 16GiB, source: vendor_spec}
          slot_ids: {value: [ChannelA-DIMM0, ChannelB-DIMM0], source: dmi}
components:
  - {id: r1, kind: ram, location: {host: box-a, slot: ChannelB-DIMM0}}
`)
	if got := MemorySlotIDs(f.Hosts[0]); got[0] != "ChannelA-DIMM0" {
		t.Fatalf("slot_ids must win: %v", got)
	}
	parseErr(t, partsBase+`      ceilings:
        memory:
          slots: {value: 4, source: dmi}
`, "disagrees with hardware.memory.slots")
	parseErr(t, partsBase+`      ceilings:
        memory:
          slots: {value: 2, source: guess}
`, "ceilings.memory.slots.source: must be one of")
}

func TestCeilingsCPUAndPCIe(t *testing.T) {
	parseOK(t, partsBase+`      ceilings:
        cpu:
          socket: {value: FT4, source: dmi}
          installed: {cores: 4, threads: 4, tdp_w: 35, source: dmi}
          max_supported: {cores: 4, threads: 4, tdp_w: 35, source: vendor_spec}
        pcie_slots:
          - {id: pcie0, gen: 3, lanes: 16, max_power_w: 75, source: vendor_spec}
        gpu:
          power_budget_w: {value: 75, source: vendor_spec}
`)
	parseErr(t, partsBase+`      ceilings:
        cpu:
          installed: {cores: 8, source: dmi}
          max_supported: {cores: 4, source: vendor_spec}
`, "installed: exceeds max_supported")
	parseErr(t, partsBase+`      ceilings:
        pcie_slots:
          - {id: pcie0, source: dmi}
          - {id: pcie0, source: dmi}
`, `duplicate slot "pcie0"`)
}

func TestOversizedDiskIsAWarningNotAnError(t *testing.T) {
	f := parseOK(t, partsBase+`      storage_slots:
        - {id: s0, kind: sata_25, max_size: {value: 500GB, source: dmi}, occupant_disk: nvme0n1}
`)
	if w := SlotWarnings(f.Hosts[0]); len(w) != 1 || !strings.Contains(w[0], "may be wrong") {
		t.Fatalf("warnings: %v", w)
	}
}
