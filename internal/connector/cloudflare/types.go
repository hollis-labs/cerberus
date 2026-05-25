package cloudflare

// Zone is the normalized view of a Cloudflare zone.
type Zone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Paused      bool     `json:"paused"`
	NameServers []string `json:"name_servers"`
	Plan        string   `json:"plan"`
}

const (
	ZoneTypeFull    = "full"
	ZoneTypePartial = "partial"
)

// DNSRecord is the normalized view of a Cloudflare DNS record.
type DNSRecord struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // A, AAAA, CNAME, MX, TXT
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority,omitempty"` // for MX records
}

// Tunnel is the normalized view of a Cloudflare Tunnel.
type Tunnel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}
