//go:build linux

package outpostexecutor

import (
	"reflect"
	"testing"
)

func TestServingUnitsComeFromSupervisorCgroup(t *testing.T) {
	for _, cgroup := range []string{"0::/user.slice/user-1000.slice/user@1000.service/app.slice/custom-outpost.service/plugin\n", "1:name=systemd:/user.slice/user-1000.slice/user@1000.service/app.slice/custom-outpost.service/plugin\n"} {
		want := []string{"user@1000.service", "custom-outpost.service"}
		if got := systemdUnitsFromCgroup(cgroup); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v", got)
		}
	}
}
