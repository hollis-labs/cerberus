package hostdump

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

// The tags keep what an untyped comparison would merge: an integer and a real of the same value,
// NULL and the empty string, text and its bytes.
func TestTagsAreTypeExact(t *testing.T) {
	seen := map[string]any{}
	for _, v := range []any{int64(1), float64(1), nil, "", []byte(""), "1", float64(0.1)} {
		tag, err := Tag(v)
		if err != nil {
			t.Fatal(err)
		}
		if prev, dup := seen[tag]; dup {
			t.Fatalf("%#v and %#v tag the same (%s)", prev, v, tag)
		}
		seen[tag] = v
	}
	if _, err := Tag(struct{}{}); err == nil {
		t.Fatal("an unknown type was tagged")
	}
}

// The digest depends on the rows and their types, not on the order they were written in.
func TestDigestIsOrderedAndTyped(t *testing.T) {
	ctx := context.Background()
	mk := func(rows ...string) string {
		t.Helper()
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		for _, tbl := range Tables {
			if _, cerr := db.Exec("CREATE TABLE " + tbl.Name + " (" + tbl.ColumnList() + ")"); cerr != nil {
				t.Fatal(cerr)
			}
		}
		for _, r := range rows {
			if _, ierr := db.Exec(r); ierr != nil {
				t.Fatal(ierr)
			}
		}
		d, err := Digest(ctx, db, "")
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	a := mk(`INSERT INTO host_samples (host, ts, load1) VALUES ('a', 1, 0.5)`, `INSERT INTO host_samples (host, ts, load1) VALUES ('b', 2, 1.5)`)
	b := mk(`INSERT INTO host_samples (host, ts, load1) VALUES ('b', 2, 1.5)`, `INSERT INTO host_samples (host, ts, load1) VALUES ('a', 1, 0.5)`)
	c := mk(`INSERT INTO host_samples (host, ts, load1) VALUES ('a', 1, 0.5)`, `INSERT INTO host_samples (host, ts, load1) VALUES ('b', 2, 2)`)
	d := mk(`INSERT INTO host_samples (host, ts, load1) VALUES ('a', 1, 0.5)`, `INSERT INTO host_samples (host, ts, load1) VALUES ('b', '2', 1.5)`)
	if a != b {
		t.Fatal("insertion order changed the digest")
	}
	if a == c || a == d {
		t.Fatal("a changed value or type left the digest equal")
	}
}
