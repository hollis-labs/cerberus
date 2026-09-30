// Package redact removes credential material from operator-facing diagnostics.
package redact

import (
	"bytes"
	"encoding/json"
	"html"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const Marker = "[REDACTED]"

var (
	privateKey  = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
	bearer      = regexp.MustCompile(`(?i)\bBearer[ \t]+([a-z0-9._~+/=-]+)`)
	providerKey = regexp.MustCompile(`\b(?:sk-(?:proj-|ant-)?[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[A-Z0-9]{16})\b`)
	assignment  = regexp.MustCompile(`(?i)(["']?[a-z0-9_.-]*(?:api[_-]?key|token|secret|password|passwd|passcode|private[_-]?key|credentials?|authorization|cookie)[a-z0-9_.-]*["']?\s*(?:=>|=|:)\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;&<>]+)`)
	// The flag rule anchors on a word boundary before the dash. Without it,
	// `--?` was satisfied by any internal hyphen, so "set X-API-Key header on
	// the tunnel" lost the word "header": the hyphen in the *middle* of a
	// header name read as a command-line flag. A real flag is always at the
	// start of the text or preceded by whitespace or an opening delimiter.
	// The boundary is part of group 1 so the replacement writes it back.
	flag       = regexp.MustCompile(`(?i)((?:^|[\s"'` + "`" + `([{])--?[a-z0-9_-]*(?:api[_-]?key|token|secret|password|passwd|private[_-]?key|credentials?)[a-z0-9_-]*(?:=|[ \t]+))("[^"\n]*"|'[^'\n]*'|[^\s,;<>]+)`)
	userinfo   = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@]+:[^@\s/]+@`)
	plistArgs  = regexp.MustCompile(`(?s)(<key>ProgramArguments</key>\s*<array>)(.*?)(</array>)`)
	plistArg   = regexp.MustCompile(`(?s)<string>(.*?)</string>`)
	plistValue = regexp.MustCompile(`(?s)(<key>([^<]+)</key>\s*<string>)(.*?)(</string>)`)
)

// looksLikeToken reports whether the text following "Bearer" is plausibly a
// credential rather than an ordinary word.
//
// The rule exists because redacting everything after "Bearer" destroyed the
// guidance that tells an operator what to do: "accepts a Bearer JWT only"
// became "accepts a Bearer [REDACTED] only", and "use Bearer auth" became "use
// Bearer [REDACTED]". A safety net that eats the instruction is worse than no
// instruction.
//
// A credential is either long, or contains something other than letters —
// digits, dots, dashes, underscores, base64 padding. Words like "JWT", "auth",
// "token" and "credentials" are short and purely alphabetic and are left alone.
// The realistic leak, `Authorization: Bearer <token>`, is matched here and is
// additionally covered by the assignment rule on "authorization".
func looksLikeToken(value string) bool {
	if len(value) >= 20 {
		return true
	}
	for _, r := range value {
		if !unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// referenceSchemes are the secret reference schemes: a value that is a
// reference names a credential and is not one. It mirrors secretref.Schemes,
// which cannot be imported here; TestRedactKnowsEveryReferenceScheme in
// internal/secretref holds the two together.
var referenceSchemes = []string{"keychain://", "keyring://", "helper://", "op://", "keeper://"}

// IsReference reports whether value is a secret reference.
func IsReference(value string) bool {
	for _, scheme := range referenceSchemes {
		if strings.HasPrefix(value, scheme) {
			return true
		}
	}
	return false
}

func SensitiveKey(key string) bool {
	key = strings.ToUpper(strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.TrimSpace(key)))
	for _, marker := range []string{"APIKEY", "APITOKEN", "ACCESSTOKEN", "AUTHTOKEN", "SECRET", "PASSWORD", "PASSWD", "PASSCODE", "PRIVATEKEY", "CREDENTIAL", "AUTHORIZATION", "COOKIE"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return key == "TOKEN" || strings.HasSuffix(key, "TOKEN")
}

// namesOnlyKeys are fields whose values are credential *names*, never credential
// values. They exist to tell an operator which credential is missing, and
// SensitiveKey matches them on the very word that makes them useful:
// "missing_secrets" contains SECRET, so a list of names came out as
// ["[REDACTED]"] — an answer to "which one?" that refuses to say which one.
//
// Add a key here only when the field is structurally incapable of holding a
// value. A field that *might* carry one belongs nowhere near this list.
var namesOnlyKeys = map[string]bool{
	"missing_secrets": true,
	// credential_sources maps a credential's name to where it came from
	// ("mapping:op via onepassword@0.1.0"): names by construction.
	"credential_sources": true,
}

// NamesOnlyKey reports whether a JSON key carries credential names rather than
// credential values, and so must survive redaction intact.
func NamesOnlyKey(key string) bool {
	return namesOnlyKeys[strings.ToLower(strings.TrimSpace(key))]
}

// errorCodes is the same idea as namesOnlyKeys applied to the left of the
// colon. Cerberus renders a failure as "<connector> <operation>: <code>: <err>",
// and "credential_missing" contains "credential", so the assignment rule read
// the code as an assignment key and ate the first word of the message after
// it: "credential_missing: reload the plugin" became "credential_missing:
// [REDACTED] the plugin", destroying the recovery instruction.
//
// An error code is a name by construction — the control plane emits it from a
// constant and nothing downstream can make it carry a credential value. The
// list covers the whole ExternalConnectorErrorCode vocabulary rather than only
// the one word that collides today, so a code added later cannot reintroduce
// this.
var errorCodes = map[string]bool{
	"connector_unavailable":   true,
	"credential_missing":      true,
	"operation_unsupported":   true,
	"invalid_args":            true,
	"acknowledgment_required": true,
	"preview_unsupported":     true,
	"operation_failed":        true,
	"audit_unavailable":       true,
	"plugin_changed":          true,
	"principal_refused":       true,
	"policy_denied":           true,
	"approval_required":       true,
	"approval_pending":        true,
	"approval_expired":        true,
	"plan_stale":              true,
	"egress_refused":          true,
	"lockdown":                true,
	"frozen":                  true,
	"session_suspended":       true,
	"deadline_exceeded":       true,
	"output_too_large":        true,
	"insufficient_scope":      true,
}

// names are connector and plugin ids: names by construction, which the
// assignment rule must not read as a key. A plugin id such as onepassword
// contains "password", so "plugin \"onepassword\": does not declare ..." read
// as an assignment and lost the word after the colon. Registered by the
// connector registry and the plugin host as each id becomes known.
var names sync.Map

// RegisterNames records connector or plugin ids as names by construction.
func RegisterNames(ids ...string) {
	for _, id := range ids {
		if id = strings.ToLower(strings.TrimSpace(id)); id != "" {
			names.Store(id, true)
		}
	}
}

// isRegisteredName reports whether an assignment rule's captured key is a
// registered id in rendered prose, and the word after it prose too. Both
// halves must agree: an id that happens to be a credential key name, such as
// a plugin called api_key, must not carry a token past the rule, so a
// token-shaped value after it is still redacted.
func isRegisteredName(key, value string) bool {
	key = strings.TrimSpace(key)
	key = strings.TrimRight(key, " \t:=>")
	key = strings.Trim(key, "\"'")
	if _, ok := names.Load(strings.ToLower(strings.TrimSpace(key))); !ok {
		return false
	}
	value = strings.Trim(value, "\"'")
	return strings.HasSuffix(value, ":") || !looksLikeToken(value)
}

// IsErrorCode reports whether code is one of Cerberus's own error codes,
// which redaction leaves alone (a test holds this list to the vocabulary).
func IsErrorCode(code string) bool { return errorCodes[code] }

// isErrorCode reports whether an assignment rule's captured key is really a
// Cerberus error code in rendered prose rather than a key with a value.
func isErrorCode(key string) bool {
	key = strings.TrimSpace(key)
	key = strings.TrimRight(key, " \t:=>")
	key = strings.Trim(key, "\"'")
	return errorCodes[strings.ToLower(strings.TrimSpace(key))]
}

type Redactor struct {
	values []string
	// rendered is a Scope's rendered set, snapshotted with its values: text
	// that gets values removed and no rules.
	rendered map[string]struct{}
}

func (r Redactor) isRendered(value string) bool {
	_, ok := r.rendered[value]
	return ok
}

// renderedAfterValues reports whether text is a rendered string after the
// values are removed from it, which is how it looks once an edge wrote it.
func (r Redactor) renderedAfterValues(text string) bool {
	for rendered := range r.rendered {
		if r.ReplaceValues(rendered) == text {
			return true
		}
	}
	return false
}

// With is r that also removes values. r is unchanged.
func (r Redactor) With(values ...string) Redactor {
	combined := New(append(append([]string(nil), r.values...), values...)...)
	combined.rendered = r.rendered
	return combined
}

// New also removes known credential values when they appear without a label.
func New(values ...string) Redactor {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !IsReference(value) {
			filtered = append(filtered, value)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return len(filtered[i]) > len(filtered[j]) })
	return Redactor{values: filtered}
}

func FromEnv(env []string) Redactor {
	var values []string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && SensitiveKey(key) && value != "" && !IsReference(value) {
			values = append(values, value)
		}
	}
	return New(values...)
}

// redactPairs applies one key/value rule, replacing each captured value with
// the marker.
// typeNamePrefix is a Go error prefix: an UpperCamelCase type name of two or
// more words, then a colon and whitespace, as fmt.Errorf("%s: %w") renders
// it. azidentity writes "AzureCLICredential: ERROR: AADSTS50076: ...", and a
// name that ends in Credential or Token satisfies the assignment rule, so the
// message's first word was eaten as if it were the credential's value. A key
// that is quoted, lower-case, one word, or joined with "=" is not this shape
// and stays an assignment.
var typeNamePrefix = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*[a-z][A-Za-z0-9]*[A-Z][A-Za-z0-9]*:[ \t]+$`)

// isTypeNamePrefix reports whether an assignment rule's match is a Go type
// name introducing prose rather than a key introducing a value. Both halves
// must agree: the key has the type-name shape, and what follows reads as the
// start of a message — another prefix ("ERROR:", "AADSTS50076:") or an
// ordinary word — not a token. A token-shaped value after such a name, as in
// "ClientSecret: 8Q~kX...", is still redacted.
func isTypeNamePrefix(key, value string) bool {
	if !typeNamePrefix.MatchString(key) {
		return false
	}
	return strings.HasSuffix(value, ":") || !looksLikeToken(value)
}

func redactPairs(pattern *regexp.Regexp, value string) string {
	return pattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := pattern.FindStringSubmatch(match)
		if IsReference(strings.Trim(parts[2], "\"'")) {
			return match
		}
		if isErrorCode(parts[1]) {
			// The code is prose, not an assignment key, so its "value" is
			// the next word of a sentence and must survive. The match has
			// already consumed that text, though, so hand it back to the
			// same rule: a real assignment sitting behind the code — as in
			// "credential_missing: API_KEY=..." — must not ride through on
			// the exemption.
			return parts[1] + redactPairs(pattern, parts[2])
		}
		if isRegisteredName(parts[1], parts[2]) {
			// A connector or plugin id, then the next word of a sentence:
			// hand the rest back, as for an error code.
			return parts[1] + redactPairs(pattern, parts[2])
		}
		if pattern == assignment && isTypeNamePrefix(parts[1], parts[2]) {
			// Same hand-back as an error code: the word after the type name
			// is prose, and anything assignment-shaped behind it is not.
			return parts[1] + redactPairs(pattern, parts[2])
		}
		return parts[1] + Marker
	})
}

func Text(value string) string { return (Redactor{}).Text(value) }

// ReplaceValues removes r's known values and applies none of the rules.
func (r Redactor) ReplaceValues(value string) string {
	for _, secret := range r.values {
		if len(secret) >= 4 {
			value = strings.ReplaceAll(value, secret, Marker)
		} else if value == secret {
			value = Marker
		}
	}
	return value
}

func (r Redactor) Text(value string) string {
	if r.isRendered(value) {
		return r.ReplaceValues(value)
	}
	value = r.ReplaceValues(value)
	value = privateKey.ReplaceAllString(value, Marker)
	value = providerKey.ReplaceAllString(value, Marker)
	value = bearer.ReplaceAllStringFunc(value, func(match string) string {
		parts := bearer.FindStringSubmatch(match)
		if !looksLikeToken(parts[1]) {
			return match
		}
		return "Bearer " + Marker
	})
	value = userinfo.ReplaceAllString(value, "${1}"+Marker+"@")
	for _, pattern := range []*regexp.Regexp{assignment, flag} {
		value = redactPairs(pattern, value)
	}
	value = plistArgs.ReplaceAllStringFunc(value, func(block string) string {
		parts := plistArgs.FindStringSubmatch(block)
		matches := plistArg.FindAllStringSubmatch(parts[2], -1)
		args := make([]string, len(matches))
		for i, match := range matches {
			args[i] = html.UnescapeString(match[1])
		}
		safe := r.Args(args)
		i := 0
		body := plistArg.ReplaceAllStringFunc(parts[2], func(original string) string {
			current := i
			i++
			if safe[current] == args[current] {
				return original
			}
			return "<string>" + html.EscapeString(safe[current]) + "</string>"
		})
		return parts[1] + body + parts[3]
	})
	return plistValue.ReplaceAllStringFunc(value, func(match string) string {
		parts := plistValue.FindStringSubmatch(match)
		if SensitiveKey(parts[2]) && !IsReference(parts[3]) {
			return parts[1] + Marker + parts[4]
		}
		return match
	})
}

func (r Redactor) Args(args []string) []string {
	out := append([]string(nil), args...)
	hideNext := false
	for i, arg := range args {
		if hideNext {
			if !IsReference(arg) {
				out[i] = Marker
			}
			hideNext = false
			continue
		}
		out[i] = r.Text(arg)
		if strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") && SensitiveKey(strings.TrimLeft(arg, "-")) {
			hideNext = true
		}
	}
	return out
}

func Launchd(value string) string {
	lines := strings.Split(value, "\n")
	inArgs, hideNext := false, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "arguments = {") {
			inArgs = true
			continue
		}
		if inArgs && trimmed == "}" {
			inArgs = false
			hideNext = false
		}
		if inArgs {
			prefix, argument, hasIndex := strings.Cut(line, " = ")
			if !hasIndex {
				prefix = line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				argument = trimmed
			} else {
				prefix += " = "
			}
			if hideNext {
				lines[i] = prefix + Marker
				hideNext = false
				continue
			}
			if strings.HasPrefix(argument, "-") && !strings.Contains(argument, "=") && SensitiveKey(strings.TrimLeft(argument, "-")) {
				hideNext = true
			}
		}
	}
	return Text(strings.Join(lines, "\n"))
}

// Marshal redacts data while preserving valid JSON and numbers.
// Schema metadata is traversed as metadata, not as credential assignments.
func Marshal(value any) ([]byte, error) { return (Redactor{}).Marshal(value) }
func (r Redactor) Marshal(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return r.JSON(data)
}
func MarshalIndent(value any, prefix, indent string) ([]byte, error) {
	return (Redactor{}).MarshalIndent(value, prefix, indent)
}
func (r Redactor) MarshalIndent(value any, prefix, indent string) ([]byte, error) {
	data, err := r.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err = json.Indent(&out, data, prefix, indent); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func JSON(data []byte) ([]byte, error) { return (Redactor{}).JSON(data) }
func (r Redactor) JSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(r.walkRoot(value))
}
func (r Redactor) walk(value any, hide, schema bool) any {
	switch v := value.(type) {
	case string:
		if hide && !schema && !IsReference(v) {
			return Marker
		}
		return r.Text(v) // a rendered string loses values only (Text)
	case json.Number:
		if hide && !schema {
			return Marker
		}
	case map[string]any:
		// Connector credential descriptors contain names/labels, never values.
		if _, name := v["name"]; name {
			if _, desc := v["description"]; desc {
				if _, hasValue := v["value"]; !hasValue {
					hide = false
				}
			}
		}
		for key, item := range v {
			v[key] = r.walkEntry(key, item, hide, schema)
		}
	case []any:
		for i, item := range v {
			v[i] = r.walk(item, hide, schema)
		}
	}
	return value
}

// walkEntry walks one key's value in an object: the command keys through
// Args, a names-only map by its entries, and everything else hidden when the
// key is credential-shaped or the object already was.
func (r Redactor) walkEntry(key string, item any, hide, schema bool) any {
	if key == "command" || key == "args" || key == "program_arguments" {
		if list, ok := item.([]any); ok {
			var args []string
			for _, arg := range list {
				if s, ok := arg.(string); ok {
					args = append(args, s)
				}
			}
			if len(args) == len(list) {
				return r.Args(args)
			}
		}
	}
	// A names-only map (credential_sources) is keyed by credential names:
	// its keys say which credential, not that a value follows, so they add
	// nothing to hide. Its values still pass the rules and the known values.
	if child, ok := item.(map[string]any); ok && NamesOnlyKey(key) {
		for name, entry := range child {
			child[name] = r.walk(entry, hide, schema)
		}
		return child
	}
	// A names-only key suppresses only its own contribution to hide. An
	// inherited hide still wins: a names-only field nested under something
	// already hidden stays hidden.
	return r.walk(item, hide || (SensitiveKey(key) && !NamesOnlyKey(key)), schema || strings.HasSuffix(key, "_schema"))
}

// walkRoot walks a whole response. A declared-secrets list is exempt from
// its key's hiding only where the response's schema is Cerberus's own and
// proves the list holds requirements, never values: a connector definition
// (a list of them, as /connectors answers, or one, as connector_describe
// does) and its config.secrets, and the console's credential editor
// (providers[].secrets). Anchored at the root, so a "secrets" key inside an
// operation's result, a plugin's or a vendor's, gets the ordinary walk.
func (r Redactor) walkRoot(value any) any {
	switch v := value.(type) {
	case []any:
		for i, item := range v {
			if def, ok := item.(map[string]any); ok && isDefinition(def) {
				v[i] = r.walkDefinition(def)
				continue
			}
			v[i] = r.walk(item, false, false)
		}
		return v
	case map[string]any:
		if isDefinition(v) {
			return r.walkDefinition(v)
		}
		if providers, ok := v["providers"].([]any); ok && len(v) <= 2 {
			for key, item := range v {
				if key != "providers" {
					v[key] = r.walkEntry(key, item, false, false)
				}
			}
			for i, item := range providers {
				provider, ok := item.(map[string]any)
				if !ok {
					providers[i] = r.walk(item, false, false)
					continue
				}
				for key, field := range provider {
					if list, ok := field.([]any); ok && key == "secrets" {
						provider[key] = r.walkRequirements(list)
						continue
					}
					provider[key] = r.walkEntry(key, field, false, false)
				}
			}
			return v
		}
	}
	return r.walk(value, false, false)
}

// isDefinition reports an object shaped as a contract.Definition: an id,
// operations and a config.
func isDefinition(v map[string]any) bool {
	_, id := v["id"]
	_, ops := v["operations"]
	_, cfg := v["config"].(map[string]any)
	return id && ops && cfg
}

func (r Redactor) walkDefinition(def map[string]any) any {
	for key, item := range def {
		if cfg, ok := item.(map[string]any); ok && key == "config" {
			for ck, citem := range cfg {
				if list, ok := citem.([]any); ok && ck == "secrets" {
					cfg[ck] = r.walkRequirements(list)
					continue
				}
				cfg[ck] = r.walkEntry(ck, citem, false, false)
			}
			continue
		}
		def[key] = r.walkEntry(key, item, false, false)
	}
	return def
}

func (r Redactor) walkRequirements(list []any) []any {
	for i, entry := range list {
		if requirement, ok := entry.(map[string]any); ok {
			list[i] = r.walkSecretRequirement(requirement, false)
		} else {
			list[i] = r.walk(entry, true, false)
		}
	}
	return list
}

// requirementNameKeys are the fields of a declared secret that are names by
// the schema of a secret requirement (pkg/connector SecretRequirement): which
// secret, what kind it is, and the variable it is read from. A requirement
// declares a secret; it never holds one.
var requirementNameKeys = map[string]bool{"name": true, "kind": true, "env": true}

// walkSecretRequirement walks one entry of a declared-secrets list. Its name,
// kind and env are exempt from the key's hiding by schema, whatever sibling
// fields are present, and still lose any known value and anything the rules
// match. Every other field is walked as any object's is, so a
// credential-shaped sibling (password, token) stays hidden, and the rest
// stays hidden unless the entry reads as a descriptor (a name and a
// description with no value). It is reached only from walkRoot's anchored
// shapes.
//
// Before this, only the descriptor rule kept an entry, so a secret declared
// with no description (the description is optional) came back over the
// socket as name "[REDACTED]", and the console's credential editor offered a
// field nobody could name.
func (r Redactor) walkSecretRequirement(v map[string]any, schema bool) any {
	hideRest := true
	if _, name := v["name"]; name {
		if _, desc := v["description"]; desc {
			if _, hasValue := v["value"]; !hasValue {
				hideRest = false
			}
		}
	}
	for key, item := range v {
		if requirementNameKeys[strings.ToLower(key)] {
			v[key] = r.walk(item, false, schema)
			continue
		}
		// Every other field is walked as any object's is: a
		// credential-shaped key such as password or token stays hidden
		// beside a descriptor's name and description.
		v[key] = r.walkEntry(key, item, hideRest, schema)
	}
	return v
}

type redactedError struct {
	source   error
	redactor Redactor
}

func (e redactedError) Error() string { return e.redactor.Text(e.source.Error()) }
func (e redactedError) Unwrap() error { return e.source }
func (r Redactor) Error(err error) error {
	if err == nil {
		return nil
	}
	return redactedError{source: err, redactor: r}
}
func Error(err error) error { return (Redactor{}).Error(err) }
