package hosts

import (
	"strings"
	"testing"
	"time"
)

const inventoryFixture = `sudo=1
sys_vendor=LENOVO
product_name=10M2S08Y00
product_version=ThinkCentre M715q
board_vendor=LENOVO
board_name=30FD
bios_version=M11KT45A
bios_date=08/09/2018
chassis_asset_tag=To Be Filled By O.E.M.
cpu_model= AMD PRO A6-8570E R5, 10 COMPUTE CORES 2C+8G
serial=PC1ABCDE
board_serial=L1HF
@@dmi-processor
# dmidecode 3.3
Handle 0x0004, DMI type 4, 48 bytes
Processor Information
	Socket Designation: SOCKET 0
	Type: Central Processor
	Upgrade: Socket FT4
	Core Count: 2
	Thread Count: 2
		Characteristics:
			64-bit capable
@@dmi-memory
Handle 0x0007, DMI type 16, 23 bytes
Physical Memory Array
	Location: System Board Or Motherboard
	Error Correction Type: None
	Maximum Capacity: 32 GB
	Number Of Devices: 2

Handle 0x0008, DMI type 17, 40 bytes
Memory Device
	Size: 8192 MB
	Type: DDR4
	Speed: 2400 MT/s
	Manufacturer: Samsung
	Part Number: M471A1K43CB1-CRC
	Configured Memory Speed: 2400 MT/s

Handle 0x0009, DMI type 17, 40 bytes
Memory Device
	Size: No Module Installed
	Type: Unknown
@@lsblk
NAME="sda" SIZE="120034123776" ROTA="0" TRAN="sata" MODEL="INTEL SSDSC2BB12" SERIAL="BTWL0000"
NAME="nvme0n1" SIZE="1000204886016" ROTA="0" TRAN="nvme" MODEL="WD Blue" SERIAL="WD123"
NAME="zram0" SIZE="4294967296" ROTA="0" TRAN="" MODEL="" SERIAL=""
@@nics
enp2s0f0 6C:4B:90:59:4E:78
@@end
`

func collectFixture(t *testing.T, mutate func(string) string) Collected {
	t.Helper()
	out := inventoryFixture
	if mutate != nil {
		out = mutate(out)
	}
	c, err := ParseInventory(out)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParseInventory(t *testing.T) {
	c := collectFixture(t, nil)
	hw := c.Hardware
	if hw.Vendor != "LENOVO" || hw.Model != "10M2S08Y00" || hw.Version != "ThinkCentre M715q" || hw.Serial != "PC1ABCDE" {
		t.Fatalf("identity: %+v", hw)
	}
	if hw.AssetTag != "" || hw.Board != "LENOVO 30FD" || hw.BIOS != "M11KT45A 08/09/2018" || hw.CPUModel != "AMD PRO A6-8570E R5, 10 COMPUTE CORES 2C+8G" || hw.CPUSocket != "FT4" {
		t.Fatalf("board/cpu: %+v", hw)
	}
	m := hw.Memory
	if m == nil || m.Type != "DDR4" || m.Total != 8<<30 || m.Slots != 2 || m.SlotsUsed != 1 || m.Max != 32<<30 ||
		len(m.Modules) != 1 || m.Modules[0] != "8GiB DDR4 2400MT/s Samsung M471A1K43CB1-CRC" {
		t.Fatalf("memory: %+v", m)
	}
	if len(hw.Disks) != 2 || hw.Disks[0].Name != "sda" || hw.Disks[0].Size != 120e9 || hw.Disks[0].Type != "ssd" ||
		hw.Disks[1].Type != "nvme" || hw.Disks[1].Size != 1e12 || hw.Disks[0].Serial != "BTWL0000" {
		t.Fatalf("disks: %+v", hw.Disks)
	}
	if len(hw.NICs) != 1 || hw.NICs[0].MAC != "6c:4b:90:59:4e:78" {
		t.Fatalf("nics: %+v", hw.NICs)
	}
	if !c.Root || c.MemSlots != 2 || c.CoreCount != 2 || c.ECC == nil || *c.ECC {
		t.Fatalf("dmi facts: %+v", c)
	}
	if _, err := ParseInventory("sudo=1\n"); err == nil {
		t.Fatal("a truncated run was accepted")
	}
}

const inventoryHost = `version: 1
hosts:
  - name: box-a
    role: worker
    hardware:
      vendor: OLD
      disks: [{name: sda, size: 100GB, type: ssd}, {name: nvme0n1, size: 1TB, type: nvme}]
      notes: spare 1 TB HDD bay free
      ceilings:
        memory:
          slots: {value: 2, source: dmi}
          max_total: {value: 64GiB, source: vendor_spec}
      storage_slots:
        - {id: m2-0, kind: nvme_m2, occupant_disk: nvme0n1}
        - {id: sata-0, kind: sata_25, occupant_disk: sda}
        - {id: sata-1, kind: sata_25, occupant_component: d1}
components:
  - {id: d1, kind: disk, location: {host: box-a, slot: sata-1}}
  - {id: spare-1, kind: ram, location: {spare: true}}
`

func TestMergeKeepsWhatAnOperatorSet(t *testing.T) {
	f := parseOK(t, inventoryHost)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	changed, warns, err := MergeInventory(f, "box-a", collectFixture(t, nil), now)
	if err != nil || !changed || len(warns) != 0 {
		t.Fatalf("merge: %v %v %v", changed, warns, err)
	}
	hw := f.Hosts[0].Hardware
	if hw.Vendor != "LENOVO" || hw.RecordedAt == nil || !hw.RecordedAt.Equal(now) {
		t.Fatalf("facts not replaced: %+v", hw)
	}
	if hw.Notes != "spare 1 TB HDD bay free" || len(hw.StorageSlots) != 3 || len(f.Components) != 2 {
		t.Fatalf("operator data lost: notes=%q slots=%d components=%d", hw.Notes, len(hw.StorageSlots), len(f.Components))
	}
	mc := hw.Ceilings.Memory
	if mc.MaxTotal.Source != SourceVendorSpec || mc.MaxTotal.Value != 64<<30 {
		t.Fatalf("a vendor_spec ceiling was overwritten by DMI: %+v", mc.MaxTotal)
	}
	if mc.Slots.Source != SourceDMI || mc.Slots.Value != 2 || mc.ECC == nil || mc.ECC.Value {
		t.Fatalf("dmi ceilings: %+v", mc)
	}
	if hw.Ceilings.CPU.Socket.Value != "FT4" || hw.Ceilings.CPU.Installed.Cores != 2 {
		t.Fatalf("cpu ceilings: %+v", hw.Ceilings.CPU)
	}
	if err := Validate(f); err != nil {
		t.Fatalf("merged file invalid: %v", err)
	}
}

func TestMergeIsIdempotentAndDoesNotChurnRecordedAt(t *testing.T) {
	f := parseOK(t, inventoryHost)
	first := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if _, _, err := MergeInventory(f, "box-a", collectFixture(t, nil), first); err != nil {
		t.Fatal(err)
	}
	changed, _, err := MergeInventory(f, "box-a", collectFixture(t, nil), first.Add(time.Hour))
	if err != nil || changed {
		t.Fatalf("an identical run must change nothing: %v %v", changed, err)
	}
	if !f.Hosts[0].Hardware.RecordedAt.Equal(first) {
		t.Fatal("recorded_at moved although no fact did")
	}
	// A fact that differs moves it.
	c := collectFixture(t, func(s string) string { return strings.Replace(s, "M11KT45A", "M11KT46A", 1) })
	changed, _, _ = MergeInventory(f, "box-a", c, first.Add(2*time.Hour))
	if !changed || !f.Hosts[0].Hardware.RecordedAt.Equal(first.Add(2*time.Hour)) {
		t.Fatal("a changed BIOS was not recorded")
	}
}

func TestMergeWithoutRootKeepsRootOnlyFacts(t *testing.T) {
	f := parseOK(t, inventoryHost)
	if _, _, err := MergeInventory(f, "box-a", collectFixture(t, nil), time.Now()); err != nil {
		t.Fatal(err)
	}
	partial := collectFixture(t, func(s string) string {
		s = strings.Replace(s, "sudo=1", "sudo=0", 1)
		s = strings.Replace(s, "serial=PC1ABCDE", "serial=", 1)
		i := strings.Index(s, "@@dmi-processor")
		j := strings.Index(s, "@@lsblk")
		s = s[:i] + s[j:]
		return strings.ReplaceAll(s, `SERIAL="BTWL0000"`, `SERIAL=""`)
	})
	_, warns, err := MergeInventory(f, "box-a", partial, time.Now())
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0], "passwordless sudo") {
		t.Fatalf("warnings: %v %v", warns, err)
	}
	hw := f.Hosts[0].Hardware
	if hw.Serial != "PC1ABCDE" || hw.Memory == nil || hw.Memory.Slots != 2 || hw.CPUSocket != "FT4" || hw.Disks[0].Serial != "BTWL0000" {
		t.Fatalf("root-only facts were dropped: %+v", hw)
	}
}

func TestMergeThatBreaksASlotIsRefusedByValidation(t *testing.T) {
	f := parseOK(t, inventoryHost)
	c := collectFixture(t, func(s string) string { return strings.Replace(s, `NAME="nvme0n1"`, `NAME="nvme1n1"`, 1) })
	if _, _, err := MergeInventory(f, "box-a", c, time.Now()); err != nil {
		t.Fatal(err)
	}
	err := Validate(f)
	if err == nil || !strings.Contains(err.Error(), `storage_slots[0].occupant_disk: "nvme0n1" is not in hardware.disks`) {
		t.Fatalf("a renamed disk must surface as a slot conflict, not be silently cleared: %v", err)
	}
}

func TestInventoryCommandIsOneQuotedSh(t *testing.T) {
	if !strings.HasPrefix(InventoryCommand, "sh -c '") || strings.Contains(InventoryCommand[len("sh -c '"):len(InventoryCommand)-1], "'") && !strings.Contains(InventoryCommand, `'\''`) {
		t.Fatal("not safely quoted")
	}
}
