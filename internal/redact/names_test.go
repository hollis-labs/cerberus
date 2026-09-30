package redact

import (
	"errors"
	"strings"
	"testing"
)

// A connector or plugin id is a name by construction. An id containing a
// credential word (onepassword) before a colon read as an assignment key and
// lost the next word; a registered id no longer does.
func TestARegisteredIDIsNotAnAssignmentKey(t *testing.T) {
	const id = "widgetpassword"
	msg := `plugin "` + id + `": does not declare operation "status"`
	if Text(msg) == msg {
		t.Fatalf("the hazard is gone before registration, so this test proves nothing: %q", Text(msg))
	}
	RegisterNames(id)
	for _, text := range []string{
		msg,
		id + ": the plugin is installed but not loaded",
		`plugin "` + id + `": ` + "restart it",
	} {
		if got := Text(text); got != text {
			t.Errorf("a registered id lost the word after it:\n  %s\n  %s", text, got)
		}
	}
}

// The exemption covers prose only. An id that is itself a credential key name
// cannot carry a token past the rule.
func TestARegisteredIDDoesNotShieldAToken(t *testing.T) {
	RegisterNames("api_key")
	for _, leaked := range []string{"api_key: sk-9f8e7d6c5b4a3210fedcba98", "api_key=Zx81kQpW2mN7", `"api_key": "ghp_16C7e42F292c6912E7710c838347Ae178B4a"`} {
		if got := Text(leaked); got == leaked || strings.Contains(got, "sk-9f8e") || strings.Contains(got, "Zx81kQ") || strings.Contains(got, "ghp_16C7") {
			t.Errorf("a registered id shielded a token: %q", got)
		}
	}
}

func TestGuidanceForKeepsTheSentinel(t *testing.T) {
	sentinel := errors.New("is not loaded")
	err := GuidanceFor(sentinel, "plugin %q is not loaded", "onepassword")
	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is lost the sentinel")
	}
	if err.Error() != `plugin "onepassword" is not loaded` {
		t.Fatalf("text = %q", err.Error())
	}
}
