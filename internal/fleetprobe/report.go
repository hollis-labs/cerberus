// Package fleetprobe reports, for one host, what runs for each service and exactly
// what it was built from: the executable behind every running systemd service and
// container, its build provenance (Go build info, or the git state of an interpreted
// script), and the same for the operator's installed binaries.
//
// The probe is read-only. It never reads a process environment, a config file's
// values or a credential: provenance comes from executables, /proc metadata,
// `systemctl show` properties and `docker inspect` image metadata only.
package fleetprobe

import "time"

// Schema identifies the report format. Bump the suffix on a breaking change.
const Schema = "cerberus.fleet.probe.v1"

// Report is one host's probe result.
type Report struct {
	Schema   string    `json:"schema"`
	Host     string    `json:"host"`
	ProbedAt time.Time `json:"probed_at"`
	// Prober is the build of the cerberus binary that produced the report.
	Prober   *GoBuild   `json:"prober,omitempty"`
	Services []Service  `json:"services"`
	Binaries []Artifact `json:"binaries"`
	Warnings []string   `json:"warnings,omitempty"`
	Duration string     `json:"duration"`
}

// Service kinds.
const (
	KindSystemdUser   = "systemd-user"
	KindSystemdSystem = "systemd-system"
	KindContainer     = "container"
)

// Service is one running service: a systemd unit's main process or a container.
type Service struct {
	Kind string `json:"kind"`
	// Name is the unit name (without .service) or the container name.
	Name string `json:"name"`
	// ResourceID is the Cerberus resource this service belongs to, when the
	// host's Cerberus registry declares one.
	ResourceID string    `json:"resource_id,omitempty"`
	Project    string    `json:"project,omitempty"`
	PID        int       `json:"pid,omitempty"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	// Artifact is what the main process executes (systemd services).
	Artifact *Artifact `json:"artifact,omitempty"`
	// Children are distinct executables running under the main process, such
	// as stdio MCP servers a daemon spawned. Shells and sandbox wrappers are
	// skipped.
	Children []Artifact `json:"children,omitempty"`
	// Image is the container image (containers).
	Image   *Image   `json:"image,omitempty"`
	Compose *Compose `json:"compose,omitempty"`
	// Health is a declared health URL's answer, when the resource declares one.
	Health *Health `json:"health,omitempty"`
}

// Artifact kinds.
const (
	ArtifactGo     = "go"
	ArtifactELF    = "elf"
	ArtifactScript = "script"
	ArtifactOther  = "other"
)

// Artifact identifies one executable.
type Artifact struct {
	Kind string `json:"kind"`
	// Path is the path as invoked or listed; ResolvedPath follows symlinks.
	Path         string    `json:"path"`
	ResolvedPath string    `json:"resolved_path,omitempty"`
	SHA256       string    `json:"sha256,omitempty"`
	Size         int64     `json:"size,omitempty"`
	ModTime      time.Time `json:"mtime,omitzero"`
	// ReplacedOnDisk is true when a running process's executable was replaced
	// or removed after it started: the process runs a build that is no longer
	// the one on disk (a restart is pending).
	ReplacedOnDisk bool `json:"replaced_on_disk,omitempty"`
	// PIDs of the processes running this artifact (children only).
	PIDs    []int    `json:"pids,omitempty"`
	Go      *GoBuild `json:"go,omitempty"`
	Script  *Script  `json:"script,omitempty"`
	Release *Release `json:"release,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// GoBuild is the provenance embedded in a Go binary.
type GoBuild struct {
	GoVersion     string    `json:"go_version"`
	Path          string    `json:"path"`
	Module        string    `json:"module"`
	ModuleVersion string    `json:"module_version,omitempty"`
	VCS           string    `json:"vcs,omitempty"`
	Revision      string    `json:"revision,omitempty"`
	Time          time.Time `json:"time,omitzero"`
	// Modified is true when the binary was built from a tree with
	// uncommitted changes.
	Modified bool `json:"modified,omitempty"`
}

// Unstamped reports a binary that carries no source identity: built from a
// work tree without VCS stamping (-buildvcs=false, or outside the repository)
// and not installed by module version. Nothing in it says which commit it is.
func (g *GoBuild) Unstamped() bool {
	return g.Revision == "" && (g.ModuleVersion == "" || g.ModuleVersion == "(devel)")
}

// Script is an interpreted service: the interpreter and the script it runs.
type Script struct {
	Interpreter string   `json:"interpreter"`
	Path        string   `json:"path"`
	SHA256      string   `json:"sha256,omitempty"`
	Git         *GitInfo `json:"git,omitempty"`
}

// GitInfo is the state of the git work tree a script lives in.
type GitInfo struct {
	Root     string `json:"root"`
	Remote   string `json:"remote,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Revision string `json:"revision"`
	// Dirty is true when tracked files have uncommitted changes.
	Dirty bool `json:"dirty"`
}

// Release is the release-directory layout <...>/releases/<app>/<version>/<exe>.
type Release struct {
	App     string `json:"app"`
	Version string `json:"version"`
	Dir     string `json:"dir"`
}

// Image is a container's image.
type Image struct {
	Ref     string    `json:"ref"`
	ID      string    `json:"id"`
	Digests []string  `json:"digests,omitempty"`
	Created time.Time `json:"created,omitzero"`
	// Version is the org.opencontainers.image.version label, when set.
	Version string `json:"version,omitempty"`
	// Revision is the org.opencontainers.image.revision label, when set.
	Revision string `json:"revision,omitempty"`
	Source   string `json:"source,omitempty"`
	State    string `json:"state,omitempty"`
	Health   string `json:"health,omitempty"`
}

// Compose is a container's docker compose project and service.
type Compose struct {
	Project    string `json:"project"`
	Service    string `json:"service"`
	WorkingDir string `json:"working_dir,omitempty"`
}

// Health is the result of a GET against a declared health URL.
type Health struct {
	URL       string `json:"url"`
	Status    int    `json:"status,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}
