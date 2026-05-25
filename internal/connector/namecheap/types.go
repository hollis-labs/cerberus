package namecheap

// Domain is the normalized view of a Namecheap domain.
type Domain struct {
	Name       string `json:"name"`
	Expires    string `json:"expires"`
	IsExpired  bool   `json:"is_expired"`
	IsLocked   bool   `json:"is_locked"`
	AutoRenew  bool   `json:"auto_renew"`
	WhoisGuard string `json:"whois_guard"`
}

// DNSRecord is a single DNS host record for a domain.
type DNSRecord struct {
	ID     int    `json:"id"`
	Type   string `json:"type"` // A, AAAA, CNAME, MX, TXT, NS
	Host   string `json:"host"`
	Value  string `json:"value"`
	TTL    int    `json:"ttl"`
	MXPref int    `json:"mx_pref,omitempty"`
}

// DomainStatus is the detailed status of a single domain.
type DomainStatus struct {
	Domain      string   `json:"domain"`
	Registered  bool     `json:"registered"`
	Expires     string   `json:"expires"`
	NameServers []string `json:"name_servers"`
}

// DomainNameserverUpdate reports the result of a nameserver change.
type DomainNameserverUpdate struct {
	Domain      string   `json:"domain"`
	Updated     bool     `json:"updated"`
	NameServers []string `json:"name_servers"`
}
