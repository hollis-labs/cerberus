package infra

import "testing"

func TestExtractDeploymentURLPrefersProductionURL(t *testing.T) {
	output := `
Vercel CLI 50.28.0
Inspect: https://vercel.com/acme/chrispian-dev/abc123 [2s]
Production: https://chrispian-dev-7h2w9w5qf-acme.vercel.app [2s]
Queued
`

	got := extractDeploymentURL(output)
	want := "https://chrispian-dev-7h2w9w5qf-acme.vercel.app"
	if got != want {
		t.Fatalf("extractDeploymentURL() = %q, want %q", got, want)
	}
}

func TestExtractDeploymentURLSkipsDashboardURLWhenPossible(t *testing.T) {
	output := `
Inspect: https://vercel.com/acme/chrispian-dev/abc123 [2s]
https://chrispian.dev
`

	got := extractDeploymentURL(output)
	want := "https://chrispian.dev"
	if got != want {
		t.Fatalf("extractDeploymentURL() = %q, want %q", got, want)
	}
}

func TestExtractDeploymentURLHandlesANSIOutput(t *testing.T) {
	output := "\x1b[90mProduction:\x1b[0m https://chrispian-dev.vercel.app \x1b[2m[2s]\x1b[0m"

	got := extractDeploymentURL(output)
	want := "https://chrispian-dev.vercel.app"
	if got != want {
		t.Fatalf("extractDeploymentURL() = %q, want %q", got, want)
	}
}

func TestExtractDeploymentURLFallsBackToDashboardURL(t *testing.T) {
	output := `
Inspect: https://vercel.com/acme/chrispian-dev/abc123 [2s]
`

	got := extractDeploymentURL(output)
	want := "https://vercel.com/acme/chrispian-dev/abc123"
	if got != want {
		t.Fatalf("extractDeploymentURL() = %q, want %q", got, want)
	}
}

func TestParseGitHubRemote(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		owner string
		repo  string
	}{
		{name: "ssh", raw: "git@github.com:chrispian/cerberus.git", owner: "chrispian", repo: "cerberus"},
		{name: "https", raw: "https://github.com/chrispian/cerberus.git", owner: "chrispian", repo: "cerberus"},
		{name: "unsupported", raw: "https://gitlab.com/chrispian/cerberus.git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo := parseGitHubRemote(tt.raw)
			if owner != tt.owner || repo != tt.repo {
				t.Fatalf("parseGitHubRemote(%q) = (%q, %q), want (%q, %q)", tt.raw, owner, repo, tt.owner, tt.repo)
			}
		})
	}
}
