package digitalocean

import "time"

// DropletStatus is the normalized view of a DigitalOcean droplet.
type DropletStatus struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // new, active, off, archive
	Region    string    `json:"region"`
	Size      string    `json:"size"`
	Image     string    `json:"image"`
	IPv4      string    `json:"ipv4,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
