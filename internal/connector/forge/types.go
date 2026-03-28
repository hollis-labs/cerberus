package forge

// Server represents a Laravel Forge server.
type Server struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	IP       string `json:"ip_address"`
	Region   string `json:"region"`
	Size     string `json:"size"`
	PHP      string `json:"php_version"`
	Provider string `json:"provider"`
	IsReady  bool   `json:"is_ready"`
}

// Site represents a site deployed on a Forge server.
type Site struct {
	ID               int    `json:"id"`
	ServerID         int    `json:"server_id"`
	Name             string `json:"name"`
	Directory        string `json:"directory"`
	Repository       string `json:"repository"`
	Branch           string `json:"deployment_branch"`
	Status           string `json:"status"`
	DeploymentStatus string `json:"deployment_status"`
}

// Deployment represents a deployment record for a Forge site.
type Deployment struct {
	ID            int    `json:"id"`
	Status        string `json:"status"`
	StartedAt     string `json:"started_at"`
	EndedAt       string `json:"ended_at"`
	CommitHash    string `json:"commit_hash"`
	CommitAuthor  string `json:"commit_author"`
	CommitMessage string `json:"commit_message"`
}
