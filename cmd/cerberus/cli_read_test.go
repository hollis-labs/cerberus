package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/registry"
)

// stoppedObserver reports every supervised resource stopped, as the old in-process lane did for
// the fixture's dev session with nothing running.
type stoppedObserver struct{}

func (stoppedObserver) Observe(context.Context, config.ResourceDef) api.Observation {
	return api.Observation{State: "stopped"}
}

// goldenServer serves the API over the golden fixture (testdata/golden-cli/shop.cerberus.yaml,
// the same descriptor the goldens were captured with from main's binary), registered in a temp
// adopted root, without the auth guard: these tests are about output, the guard has its own.
func goldenServer(t *testing.T) *apiClient {
	t.Helper()
	root := t.TempDir()
	desc, err := filepath.Abs(filepath.Join("testdata", "golden-cli", "shop.cerberus.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.ForRoot(root)
	if _, rerr := reg.Register(desc); rerr != nil {
		t.Fatal(rerr)
	}
	a, err := api.New(api.Options{
		Resources: func() ([]config.ResourceDef, error) {
			r, rerr := reg.Resolve()
			if rerr != nil {
				return nil, rerr
			}
			return r.Config.Resources, nil
		},
		Observer: stoppedObserver{},
		Home:     t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Mux())
	t.Cleanup(srv.Close)
	return &apiClient{base: srv.URL + "/api", key: "test", keyFile: "test.key", http: srv.Client()}
}

func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden-cli", name)) //nolint:gosec // fixed test data
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// resource list, resource status and their -o json print exactly what main's CLI printed for the
// same fixture (testdata/golden-cli, captured from main's binary under a scratch HOME).
func TestReadVerbsMatchTheOldCLI(t *testing.T) {
	c := goldenServer(t)
	ctx := context.Background()

	var rows []api.ResourceInfo
	if err := c.get(ctx, "/resources", &rows); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printResourceList(&out, rows)
	if want := golden(t, "resource-list.txt"); out.String() != want {
		t.Errorf("resource list:\n got:\n%s\nwant:\n%s", out.String(), want)
	}
	out.Reset()
	if err := printJSON(&out, rows); err != nil {
		t.Fatal(err)
	}
	if want := golden(t, "resource-list.json"); out.String() != want {
		t.Errorf("resource list -o json:\n got:\n%s\nwant:\n%s", out.String(), want)
	}
	for _, id := range []string{"dev", "box"} {
		var st api.ResourceRuntimeStatus
		if err := c.get(ctx, "/resources/"+id+"/status", &st); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		printResourceRuntimeStatus(&out, st)
		if want := golden(t, "resource-status-"+id+".txt"); out.String() != want {
			t.Errorf("resource status %s:\n got:\n%s\nwant:\n%s", id, out.String(), want)
		}
	}
}

// `status` keeps the old title, section labels, order and layout ("  %-8s %s"); the values are
// this build's (later slices own most of them).
func TestStatusKeepsTheOldLabelsAndLayout(t *testing.T) {
	label := regexp.MustCompile(`^  (\S+)\s`)
	labels := func(s string) []string {
		var out []string
		for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n")[1:] {
			if m := label.FindStringSubmatch(l); m != nil {
				out = append(out, m[1])
			}
		}
		return out
	}
	c := goldenServer(t)
	var out bytes.Buffer
	writeStatus(&out, nil, statusSections(context.Background(), c))
	old := golden(t, "status.txt")
	if got, want := strings.Join(labels(out.String()), " "), strings.Join(labels(old), " "); got != want {
		t.Fatalf("labels %q, want %q", got, want)
	}
	if !strings.HasPrefix(out.String(), "Cerberus status\n") {
		t.Fatalf("title: %q", out.String())
	}
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n")[1:] {
		if len(l) < 11 || l[:2] != "  " || l[10] != ' ' {
			t.Errorf("line %q is not in the old \"  %%-8s %%s\" layout", l)
		}
	}
	if !strings.Contains(out.String(), "  daemon   running at http://127.0.0.1:") {
		t.Errorf("daemon line: %s", out.String())
	}
}

// A refused key is shown, naming the key file.
func TestRefusedKeyIsShown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized","message":"invalid credential"}`))
	}))
	defer srv.Close()
	c := &apiClient{base: srv.URL + "/api", key: "revoked", keyFile: "/x/cli.key", http: srv.Client()}
	var rows []api.ResourceInfo
	err := c.get(context.Background(), "/resources", &rows)
	if err == nil || !strings.Contains(err.Error(), "401 unauthorized: invalid credential") || !strings.Contains(err.Error(), "/x/cli.key") {
		t.Fatalf("err = %v", err)
	}
}

// A key file others can read, or that is not a regular file, is refused before it is read.
func TestKeyFileMustBePrivate(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "cli.key")
	if err := os.WriteFile(key, []byte("key_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(key)
	if err := privateKeyFile(key, fi); err != nil {
		t.Fatalf("0600: %v", err)
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604} {
		if err := os.Chmod(key, mode); err != nil {
			t.Fatal(err)
		}
		fi, _ = os.Stat(key)
		if err := privateKeyFile(key, fi); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %#o: %v, want a refusal naming chmod 600", mode, err)
		}
	}
	fi, _ = os.Stat(dir)
	if err := privateKeyFile(dir, fi); err == nil {
		t.Error("a directory was accepted as a key file")
	}
}
