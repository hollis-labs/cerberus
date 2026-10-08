package webui

import "github.com/hollis-labs/cerberus/internal/config"

// SetPublicURL configures the reverse proxy origin before Handler is called.
// It also makes session cookies Secure, including when TLS ends at the proxy.
func (s *Server) SetPublicURL(raw string) error {
	base, err := config.NormalizeWebPublicURL(raw)
	if err != nil {
		return err
	}
	s.publicURL = base
	return nil
}
