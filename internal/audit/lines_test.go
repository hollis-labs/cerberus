package audit

import (
	"strings"
	"testing"
)

// A line over the limit is one damaged line, and the lines after it are
// read (H-e).
func TestScanLinesGoesPastAnOverlongLine(t *testing.T) {
	input := "one\n" + strings.Repeat("x", 300) + "\nthree\nfour"
	var got []string
	var long int
	if err := ScanLines(strings.NewReader(input), 100, func(line []byte, tooLong bool) {
		if tooLong {
			long++
			got = append(got, "<long>")
			return
		}
		got = append(got, string(line))
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "one,<long>,three,four" || long != 1 {
		t.Fatalf("lines %v", got)
	}
}

// A line longer than the read buffer, under and over the limit.
func TestScanLinesAcrossTheReadBuffer(t *testing.T) {
	under, over := strings.Repeat("a", 150*1024), strings.Repeat("b", 300*1024)
	var lens []int
	var long int
	_ = ScanLines(strings.NewReader(under+"\n"+over+"\nend\n"), 200*1024, func(line []byte, tooLong bool) {
		if tooLong {
			long++
		}
		lens = append(lens, len(line))
	})
	if len(lens) != 3 || lens[0] != len(under) || long != 1 || lens[2] != 3 {
		t.Fatalf("lens %v long %d", lens, long)
	}
}
