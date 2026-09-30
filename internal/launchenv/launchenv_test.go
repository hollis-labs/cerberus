package launchenv

import (
	"reflect"
	"testing"
)

func TestPathKeepsAbsoluteEntriesThenBase(t *testing.T) {
	got := Path("/home/me/.local/bin::.:relative:/usr/bin:/home/me/go/bin", SystemdBasePath)
	want := "/home/me/.local/bin:/usr/bin:/home/me/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/sbin:/bin"
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestPathEmptyEnvIsBase(t *testing.T) {
	if got := Path("", LaunchdBasePath); got != LaunchdBasePath {
		t.Fatalf("Path = %q, want %q", got, LaunchdBasePath)
	}
}

func TestUserBusEnvDefaultsOnlyWhatIsUnset(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	want := []string{"XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"}
	if got := UserBusEnv(getenv, 1000); !reflect.DeepEqual(got, want) {
		t.Fatalf("UserBusEnv = %v, want %v", got, want)
	}

	env["XDG_RUNTIME_DIR"] = "/custom/run"
	want = []string{"DBUS_SESSION_BUS_ADDRESS=unix:path=/custom/run/bus"}
	if got := UserBusEnv(getenv, 1000); !reflect.DeepEqual(got, want) {
		t.Fatalf("UserBusEnv = %v, want %v", got, want)
	}

	env["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=/elsewhere"
	if got := UserBusEnv(getenv, 1000); len(got) != 0 {
		t.Fatalf("UserBusEnv = %v, want nothing when both are set", got)
	}
}
