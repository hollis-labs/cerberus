package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/dataaccess"
	"github.com/hollis-labs/cerberus/pkg/plugin"
)

// The data lane's binding (old BindDataService): a session's query is admitted only for a
// read_sensitive operation of an installed plugin granted data_query_v1, so a built-in or an
// unknown connector is refused before anything runs; and its audit pair is written on the gate's
// sink as old wrote it: operation data_query, effect read_sensitive, the session as an agent via
// mcp_http with client cerberus-data and its parent, the parameters by keyed digest only, the
// shadow decision and posture.
func TestTheDataLaneIsGatedAsOnMain(t *testing.T) {
	l := newLane(t, "", nil)
	d := &dataaccess.Service{}
	l.svc.BindDataService(d)
	session := libauth.Principal{Verified: true, ID: "agent-a", Kind: libauth.KindAgent, SessionID: "session-a", TokenID: "token-a", Parent: "parent-a", Scopes: []string{dataaccess.Scope}}
	for _, connector := range []string{"ssh", "postgres-data"} {
		g := dataaccess.Grant{ID: "g", Issuer: "https://tether.test", Connector: connector, Target: "fixture", Projection: "documents", Version: "v1", ExpiresAt: time.Now().Add(time.Minute)}
		if err := d.Authorize(context.Background(), session, g); !errors.Is(err, dataaccess.ErrDenied) {
			t.Errorf("%s: %v", connector, err)
		}
	}
	q := plugin.DataQuery{Target: "fixture", Projection: "documents", Version: "v1", Parameters: json.RawMessage(`{"tenant":"private value"}`)}
	g := dataaccess.Grant{ID: "g", Issuer: "https://tether.test", Connector: "postgres-data", Target: "fixture", Projection: "documents", Version: "v1"}
	if err := d.Audit(dataaccess.Event{Kind: audit.KindIntent, ID: "op1", Principal: session, Issuer: g.Issuer, Grant: g, Query: q}); err != nil {
		t.Fatal(err)
	}
	recs := l.world.Records(audit.KindIntent)
	if len(recs) != 1 {
		t.Fatalf("records: %+v", recs)
	}
	r := recs[0]
	if r.Operation != "data_query" || r.Effect != "read_sensitive" || r.Connector != "postgres-data" || r.Principal.Kind != "agent" || r.Principal.Via != "mcp_http" ||
		r.Principal.Client != "cerberus-data" || r.Principal.OnBehalfOf != "parent-a" || r.Policy == nil || r.Posture == "" || r.OperationID != "op1" {
		t.Fatalf("the record: %+v", r)
	}
	raw, _ := json.Marshal(r)
	if bytes.Contains(raw, []byte("private value")) {
		t.Fatal("the record holds a parameter's value")
	}
}
