package cerbapi

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/hosts"
)

const inventoryOutput = `sudo=1
sys_vendor=LENOVO
product_name=10M2S08Y00
product_version=ThinkCentre M715q
board_vendor=LENOVO
board_name=30FD
bios_version=M11KT45A
bios_date=08/09/2018
cpu_model=AMD PRO A6-8570E R5
serial=PC1ABCDE
@@dmi-processor
Handle 0x0004, DMI type 4, 48 bytes
Processor Information
	Upgrade: Socket FT4
	Core Count: 2
	Thread Count: 2
@@dmi-memory
Handle 0x0007, DMI type 16, 23 bytes
Physical Memory Array
	Error Correction Type: None
	Maximum Capacity: 32 GB
	Number Of Devices: 2

Handle 0x0008, DMI type 17, 40 bytes
Memory Device
	Size: 8192 MB
	Type: DDR4
	Speed: 2400 MT/s
	Manufacturer: Samsung
@@lsblk
NAME="sda" SIZE="120034123776" ROTA="0" TRAN="sata" MODEL="INTEL SSDSC2BB12" SERIAL="BTWL0000"
@@nics
enp2s0f0 6C:4B:90:59:4E:78
@@end
`

const inventoryRegistry = `version: 1
hosts:
  - name: box-a
    role: worker
    reach: {ssh: box-a-ssh}
    hardware:
      disks: [{name: sda, size: 100GB, type: ssd}]
      notes: spare bay free
      storage_slots:
        - {id: sata-0, kind: sata_25, occupant_disk: sda}
        - {id: sata-1, kind: sata_25}
  - name: box-b
    role: worker
components:
  - {id: spare-1, kind: ram, location: {spare: true}}
`

func inventoryFixtureService(t *testing.T, backend *probeBackend) *hostFixture {
	t.Helper()
	f := newHostFixture(t, backend)
	if err := os.WriteFile(f.svc.hostsPath, []byte(inventoryRegistry), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func ack(t *testing.T, f *hostFixture) WriteArgs { return WriteArgs{Acknowledged: true} }

func TestInventoryDryRunWritesNothing(t *testing.T) {
	f := inventoryFixtureService(t, &probeBackend{stdout: inventoryOutput})
	before, _ := os.ReadFile(f.svc.hostsPath)
	res, err := f.svc.Inventory(context.Background(), InventoryArgs{WriteArgs: ack(t, f), Name: "box-a", DryRun: true})
	if err != nil || !res.DryRun || !res.Changed || res.After["hardware.vendor"] != "LENOVO" {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	if after, _ := os.ReadFile(f.svc.hostsPath); string(after) != string(before) {
		t.Fatal("a dry run changed hosts.yaml")
	}
	if !strings.Contains(f.backend.command, "dmidecode") || !strings.Contains(f.backend.command, "sudo -n") {
		t.Fatalf("the collection command: %q", f.backend.command)
	}
	if h, _ := f.svc.HostHistory(context.Background(), "box-a", 10); len(h) != 0 {
		t.Fatal("a dry run was recorded in the history")
	}
}

func TestInventoryRecordsFactsKeepsOperatorDataAndIsIdempotent(t *testing.T) {
	f := inventoryFixtureService(t, &probeBackend{stdout: inventoryOutput})
	ctx := context.Background()
	res, err := f.svc.Inventory(ctx, InventoryArgs{WriteArgs: ack(t, f), Name: "box-a"})
	if err != nil || !res.Changed || res.Revision == "" || res.Backup == "" {
		t.Fatalf("inventory: %+v %v", res, err)
	}
	v, _ := f.svc.Get(ctx, "box-a")
	hw := v.Host.Hardware
	if hw.Vendor != "LENOVO" || hw.Serial != "PC1ABCDE" || hw.Notes != "spare bay free" || len(hw.StorageSlots) != 2 || hw.Memory.Slots != 2 {
		t.Fatalf("hardware: %+v", hw)
	}
	if v.StorageSummary["sata_25"].Free != 1 || v.Ceilings.Memory.MaxTotal.Source != hosts.SourceDMI {
		t.Fatalf("view: %+v %+v", v.StorageSummary, v.Ceilings)
	}
	if comps, _ := f.svc.ListComponents(ctx, ComponentFilter{}); len(comps) != 1 {
		t.Fatal("components were lost")
	}
	hist, _ := f.svc.HostHistory(ctx, "box-a", 10)
	if len(hist) != 1 || hist[0].Op != OpHostInventory || hist[0].After["hardware.vendor"] != "LENOVO" {
		t.Fatalf("history: %+v", hist)
	}
	// Same facts again: nothing changes, nothing is recorded.
	bytesBefore, _ := os.ReadFile(f.svc.hostsPath)
	res, err = f.svc.Inventory(ctx, InventoryArgs{WriteArgs: ack(t, f), Name: "box-a"})
	if err != nil || res.Changed {
		t.Fatalf("second run: %+v %v", res, err)
	}
	if after, _ := os.ReadFile(f.svc.hostsPath); string(after) != string(bytesBefore) {
		t.Fatal("an unchanged inventory rewrote the file")
	}
	if hist, _ = f.svc.HostHistory(ctx, "box-a", 10); len(hist) != 1 {
		t.Fatalf("an unchanged inventory was recorded: %d", len(hist))
	}
}

func TestInventoryRefusals(t *testing.T) {
	ctx := context.Background()
	f := inventoryFixtureService(t, &probeBackend{stdout: inventoryOutput})
	before, _ := os.ReadFile(f.svc.hostsPath)
	for name, run := range map[string]func() error{
		"no reach.ssh": func() error {
			_, err := f.svc.Inventory(ctx, InventoryArgs{WriteArgs: ack(t, f), Name: "box-b"})
			if err == nil || !strings.Contains(err.Error(), "reach.ssh") {
				return errors.New("want a reach.ssh message, got " + errString(err))
			}
			return err
		},
		"unknown host": func() error {
			_, err := f.svc.Inventory(ctx, InventoryArgs{WriteArgs: ack(t, f), Name: "ghost"})
			return err
		},
		"no ack": func() error {
			_, err := f.svc.Inventory(ctx, InventoryArgs{Name: "box-a"})
			return err
		},
	} {
		if err := run(); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	f.backend.connectErr = errors.New("dial tcp: i/o timeout")
	if _, err := f.svc.Inventory(ctx, InventoryArgs{WriteArgs: ack(t, f), Name: "box-a"}); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("unreachable: %v", err)
	}
	f.backend.connectErr = nil
	f.backend.stdout = "sudo=1\n"
	if _, err := f.svc.Inventory(ctx, InventoryArgs{WriteArgs: ack(t, f), Name: "box-a"}); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("truncated: %v", err)
	}
	if after, _ := os.ReadFile(f.svc.hostsPath); string(after) != string(before) {
		t.Fatal("a refused inventory changed hosts.yaml")
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestInventoryOverTheSocket(t *testing.T) {
	f := inventoryFixtureService(t, &probeBackend{stdout: inventoryOutput})
	client := serveOnSocket(t, NewInProcessClient(WithHostService(f.svc)))
	res, err := client.InventoryHost(context.Background(), InventoryArgs{WriteArgs: WriteArgs{Acknowledged: true}, Name: "box-a", DryRun: true})
	if err != nil || !res.DryRun || res.After["hardware.vendor"] != "LENOVO" {
		t.Fatalf("%+v %v", res, err)
	}
}
