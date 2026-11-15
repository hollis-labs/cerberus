package audit

import "time"

// OpenFileSinkClock is OpenFileSink with the clock its records are stamped and filed by: a record
// goes to the month file of now(). It is for a test or a rehearsal that must cross a month
// boundary on a timeline it chooses (export-legacy's round trip); the daemon uses OpenFileSink.
// It lives apart from sink.go so the files the old build shares stay byte for byte main's.
func OpenFileSinkClock(dir string, now func() time.Time) (*FileSink, error) {
	if now == nil {
		now = time.Now
	}
	return openFileSink(dir, now)
}
