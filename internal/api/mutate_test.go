package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// recordingMutator answers with a fixed result or error and records the request it got.
type recordingMutator struct {
	got []supervise.Request
	out *supervise.OpResult
	err error
}

func (m *recordingMutator) Mutate(_ context.Context, req supervise.Request) (*supervise.OpResult, error) {
	m.got = append(m.got, req)
	return m.out, m.err
}

func mutatingAPI(t *testing.T, m *recordingMutator) http.Handler {
	t.Helper()
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return fixture(), nil }, Observer: fixedObserver{}, Home: t.TempDir(), Mutator: m})
	if err != nil {
		t.Fatal(err)
	}
	return a.Mux()
}

func post(t *testing.T, h http.Handler, path string, body io.Reader) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, body))
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b)
}

// The body is the old socket's: empty (no acknowledgment), or JSON with unknown fields ignored
// and confirmed_plan_hash ignored on the plain route; anything else is 400, a body cut short by
// the 1 MiB limit included (the old socket read at most 1 MiB, then decoded).
func TestMutationBodyIsDecodedAsTheOldSocketDid(t *testing.T) {
	m := &recordingMutator{out: &supervise.OpResult{Success: true, ServiceID: "web", Message: "ok"}}
	h := mutatingAPI(t, m)
	f := false
	for _, c := range []struct {
		path, body string
		want       supervise.Request
	}{
		{"/api/resources/web/reload", "", supervise.Request{Verb: "reload", ID: "web"}},
		{"/api/resources/web/deploy", `{"acknowledged":true,"install_after_build_override":false,"surprise":1}`,
			supervise.Request{Verb: "deploy", ID: "web", Acknowledged: true, InstallAfterBuildOverride: &f}},
		{"/api/resources/web/apply", `{"acknowledged":true,"approval_id":"ap-1","plan":true,"confirmed_plan_hash":"sha256:x"}`,
			supervise.Request{Verb: "apply", ID: "web", Acknowledged: true, ApprovalID: "ap-1", Plan: true}},
		{"/api/resources/nope/remove", `{"acknowledged":true}`, supervise.Request{Verb: "remove", ID: "nope", Acknowledged: true}},
	} {
		m.got = nil
		code, _ := post(t, h, c.path, strings.NewReader(c.body))
		if code != 200 || len(m.got) != 1 || !reflect.DeepEqual(m.got[0], c.want) {
			t.Errorf("POST %s %s = %d, request %+v, want %+v", c.path, c.body, code, m.got, c.want)
		}
	}
	m.got = nil
	for _, body := range []string{`{"acknowledged":`, `[1]`, `{"acknowledged":true,"pad":"` + strings.Repeat("a", 1<<20) + `"}`} {
		if code, out := post(t, h, "/api/resources/web/reload", strings.NewReader(body)); code != 400 || !strings.Contains(out, `"code":"invalid_args"`) {
			t.Errorf("body %.20q = %d %s, want 400", body, code, out)
		}
	}
	if len(m.got) != 0 {
		t.Fatalf("a bad body reached the lane: %+v", m.got)
	}
}

// A refusal before the run answers with its status and the flat {code, message}; a run, failed
// or not, answers 200 with the OpResult in the old bytes.
func TestMutationAnswersAsTheOldSocketDid(t *testing.T) {
	for _, c := range []struct {
		name string
		m    *recordingMutator
		code int
		body string
	}{
		{"failed run", &recordingMutator{out: &supervise.OpResult{ServiceID: "nope", Error: `resource "nope" not found`}}, 200,
			`{"build_performed":false,"error":"resource \"nope\" not found","service_id":"nope","success":false}` + "\n"},
		{"run", &recordingMutator{out: &supervise.OpResult{Success: true, ServiceID: "web", Message: `resource "web" reloaded successfully`}}, 200,
			`{"build_performed":false,"message":"resource \"web\" reloaded successfully","service_id":"web","success":true}` + "\n"},
		{"no ack", &recordingMutator{err: &supervise.Refusal{Status: 409, Code: "acknowledgment_required", Message: "needs --ack"}}, 409,
			`"code":"acknowledgment_required"`},
		{"approval required", &recordingMutator{err: gate.NewError("local", "reload", gate.CodeApprovalRequired, errors.New("no approval ap-1"))}, 428,
			`"code":"approval_required"`},
		{"approval pending", &recordingMutator{err: &gate.Error{Code: gate.CodeApprovalPending, Connector: "local", Operation: "reload", Err: errors.New("pending"),
			Approval: &gate.ApprovalRef{ID: "apr_1", ApproveWith: "approve it on the console", Channel: "out_of_band"}}}, 409,
			`"code":"approval_pending","message":"local reload: approval_pending: pending","details":{"approval":{"id":"apr_1","expires_at":"0001-01-01T00:00:00Z","approve_with":"approve it on the console","channel":"out_of_band"}}}`},
		{"policy denied", &recordingMutator{err: gate.NewError("local", "reload", gate.CodePolicyDenied, errors.New("denied"))}, 403,
			`"code":"policy_denied"`},
		{"lockdown", &recordingMutator{err: gate.NewError("local", "reload", gate.CodeLockdown, errors.New("LOCKDOWN"))}, 423,
			`"code":"lockdown"`},
		{"frozen", &recordingMutator{err: gate.NewError("local", "reload", gate.CodeFrozen, errors.New("FROZEN"))}, 423,
			`"code":"frozen"`},
		{"observe", &recordingMutator{err: &supervise.Refusal{Status: 409, Code: "observe_mode", Message: "observe"}}, 409,
			`"code":"observe_mode"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, body := post(t, mutatingAPI(t, c.m), "/api/resources/web/reload", strings.NewReader(`{"acknowledged":true}`))
			if code != c.code {
				t.Fatalf("status %d, want %d: %s", code, c.code, body)
			}
			if c.code == 200 && body != c.body {
				t.Fatalf("body\n got %s\nwant %s", body, c.body)
			}
			if c.code != 200 && (!strings.Contains(body, c.body) || strings.Contains(body, `"success"`)) {
				t.Fatalf("body %s, want the flat refusal with %s", body, c.body)
			}
		})
	}
}

// The lane's result, every field set, is the old OpResult byte for byte once mapped.
func TestLaneResultMapsToTheOldOpResult(t *testing.T) {
	var lane supervise.OpResult
	fill(reflect.ValueOf(&lane).Elem(), "OpResult", 0)
	lane.Plan = nil // the golden file leaves the plan out (golden_test.go)
	want, err := os.ReadFile(filepath.Join("testdata", "golden", "OpResult.full.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wire(opResult(&lane)); !bytes.Equal(got, want) {
		t.Fatalf("mapped lane result\n got %s\nwant %s", got, want)
	}
}
