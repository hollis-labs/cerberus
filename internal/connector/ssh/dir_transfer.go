package ssh

import (
	"os"

	sftpsync "github.com/hollis-labs/go-sftpsync"
)

// DirTransferResult reports what a recursive transfer moved.
//
// It is a Cerberus type rather than go-sftpsync's own Result, per
// docs/adr/0003-connector-response-dtos.md. Nothing in that library's result
// is credential-bearing, so this DTO exists to reshape rather than to exclude:
// os.FileMode marshals as an opaque integer and time.Duration as a nanosecond
// count, neither of which is readable in CLI output or in an agent's context.
// It is also the type decodeConnectorPayload decodes into, which wants a shape
// that cannot change under a dependency's release schedule.
//
// LocalPath and RemotePath name the two ends rather than source and
// destination, matching TransferResult and the config keys the CLI sends;
// Direction says which way the bytes went.
type DirTransferResult struct {
	Direction  string             `json:"direction"`
	LocalPath  string             `json:"local_path"`
	RemotePath string             `json:"remote_path"`
	DryRun     bool               `json:"dry_run"`
	Files      int                `json:"files"`
	Dirs       int                `json:"dirs"`
	Symlinks   int                `json:"symlinks"`
	Skipped    int                `json:"skipped"`
	Bytes      int64              `json:"bytes"`
	DurationMS int64              `json:"duration_ms"`
	Entries    []DirTransferEntry `json:"entries"`
}

// DirTransferEntry is one path the walk reached. Paths are relative to the
// transfer root and slash-separated on both ends, so the two sides compare.
type DirTransferEntry struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Mode   string `json:"mode,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Target string `json:"target,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// dirTransferResultFrom maps a go-sftpsync result onto the DTO. It is written
// out by hand, field by field: the mapping is the place a reviewer can see
// what Cerberus emits, and a reflection mapper would make that a property of
// whether two field names happened to agree. See ADR 0003.
func dirTransferResultFrom(res *sftpsync.Result, localPath, remotePath string) *DirTransferResult {
	if res == nil {
		return nil
	}
	out := &DirTransferResult{
		Direction:  string(res.Direction),
		LocalPath:  localPath,
		RemotePath: remotePath,
		DryRun:     res.DryRun,
		Files:      res.Files,
		Dirs:       res.Dirs,
		Symlinks:   res.Symlinks,
		Skipped:    res.Skipped,
		Bytes:      res.Bytes,
		DurationMS: res.Duration.Milliseconds(),
		Entries:    make([]DirTransferEntry, 0, len(res.Entries)),
	}
	for _, e := range res.Entries {
		out.Entries = append(out.Entries, DirTransferEntry{
			Path:   e.Path,
			Action: string(e.Action),
			Mode:   modeString(e.Mode),
			Size:   e.Size,
			Target: e.Target,
			Reason: e.Reason,
		})
	}
	return out
}

// modeString renders permission bits the way an operator reads them. A
// symlink entry carries no mode — a link's own bits are not portable and
// nothing chmods them — and rendering zero as "----------" would claim a
// permission set that was never applied.
func modeString(mode os.FileMode) string {
	if mode == 0 {
		return ""
	}
	return mode.Perm().String()
}
