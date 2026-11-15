package local

// SystemdOptions configure the systemd user backend of a connector. A zero field keeps the
// default: the user manager, the home directory, tools resolved on PATH, the serving process's
// PATH and executable. The Substrate build's supervision lane sets them so its tests can run
// against a fake systemctl and a scratch home.
type SystemdOptions struct {
	// Runner runs systemctl, journalctl and loginctl.
	Runner CommandRunner
	// Env is what a written unit depends on besides its resource (RenderSystemdUnit's input).
	Env RenderEnv
	// LookPath resolves a systemd tool; nil resolves it on PATH and the usual directories.
	LookPath func(string) (string, error)
}

// NewWithSystemd creates a local connector whose systemd user backend uses o.
func NewWithSystemd(o SystemdOptions) *Connector {
	c := New()
	b := c.service.(osServiceBackend)
	if o.Runner != nil {
		b.systemd.runner = o.Runner
	}
	if o.Env.Home != "" {
		home := o.Env.Home
		b.systemd.homeDir = func() (string, error) { return home, nil }
		b.systemd.install.homeDir = b.systemd.homeDir
	}
	if o.Env.Path != "" {
		path := o.Env.Path
		b.systemd.envPath = func() string { return path }
	}
	if o.Env.Cerberus != "" {
		self := o.Env.Cerberus
		b.systemd.selfPath = func() (string, error) { return self, nil }
	}
	if o.LookPath != nil {
		b.systemd.lookPath = o.LookPath
	}
	c.service = b
	return c
}
