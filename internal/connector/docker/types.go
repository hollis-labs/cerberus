package docker

// Container is the normalized view of a Docker container.
type Container struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Image     string   `json:"image"`
	Status    string   `json:"status"`
	State     string   `json:"state"`
	Ports     []string `json:"ports,omitempty"`
	CreatedAt string   `json:"created_at"`
}

// ComposeStack is the normalized view of a Docker Compose stack.
type ComposeStack struct {
	Name       string           `json:"name"`
	Status     string           `json:"status"`
	ConfigFile string           `json:"config_file"`
	Services   []ComposeService `json:"services"`
}

// ComposeService is a single service within a Compose stack.
type ComposeService struct {
	Name  string   `json:"name"`
	State string   `json:"state"`
	Image string   `json:"image"`
	Ports []string `json:"ports,omitempty"`
}
