// Package redact removes credential material from operator-facing diagnostics.
package redact

import (
	"bytes"
	"encoding/json"
	"html"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const Marker = "[REDACTED]"

var (
	privateKey  = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
	bearer      = regexp.MustCompile(`(?i)\bBearer[ \t]+([a-z0-9._~+/=-]+)`)
	providerKey = regexp.MustCompile(`\b(?:sk-(?:proj-|ant-)?[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[A-Z0-9]{16})\b`)
	assignment  = regexp.MustCompile(`(?i)(["']?[a-z0-9_.-]*(?:api[_-]?key|token|secret|password|passwd|passcode|private[_-]?key|credentials?|authorization|cookie)[a-z0-9_.-]*["']?\s*(?:=>|=|:)\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;&<>]+)`)
	flag        = regexp.MustCompile(`(?i)(--?[a-z0-9_-]*(?:api[_-]?key|token|secret|password|passwd|private[_-]?key|credentials?)[a-z0-9_-]*(?:=|[ \t]+))("[^"\n]*"|'[^'\n]*'|[^\s,;<>]+)`)
	userinfo    = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@]+:[^@\s/]+@`)
	plistArgs   = regexp.MustCompile(`(?s)(<key>ProgramArguments</key>\s*<array>)(.*?)(</array>)`)
	plistArg    = regexp.MustCompile(`(?s)<string>(.*?)</string>`)
	plistValue  = regexp.MustCompile(`(?s)(<key>([^<]+)</key>\s*<string>)(.*?)(</string>)`)
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

func IsReference(value string) bool {
	return strings.HasPrefix(value, "keychain://") || strings.HasPrefix(value, "helper://")
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
}

// NamesOnlyKey reports whether a JSON key carries credential names rather than
// credential values, and so must survive redaction intact.
func NamesOnlyKey(key string) bool {
	return namesOnlyKeys[strings.ToLower(strings.TrimSpace(key))]
}

type Redactor struct{ values []string }

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

func Text(value string) string { return (Redactor{}).Text(value) }
func (r Redactor) Text(value string) string {
	for _, secret := range r.values {
		if len(secret) >= 4 {
			value = strings.ReplaceAll(value, secret, Marker)
		} else if value == secret {
			value = Marker
		}
	}
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
		value = pattern.ReplaceAllStringFunc(value, func(match string) string {
			parts := pattern.FindStringSubmatch(match)
			if IsReference(strings.Trim(parts[2], "\"'")) {
				return match
			}
			return parts[1] + Marker
		})
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
func Marshal(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return JSON(data)
}
func MarshalIndent(value any, prefix, indent string) ([]byte, error) {
	data, err := Marshal(value)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err = json.Indent(&out, data, prefix, indent); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func JSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(walk(value, false, false))
}
func walk(value any, hide, schema bool) any {
	switch v := value.(type) {
	case string:
		if hide && !schema && !IsReference(v) {
			return Marker
		}
		return Text(v)
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
			if key == "command" || key == "args" || key == "program_arguments" {
				if list, ok := item.([]any); ok {
					var args []string
					for _, arg := range list {
						if s, ok := arg.(string); ok {
							args = append(args, s)
						}
					}
					if len(args) == len(list) {
						v[key] = (Redactor{}).Args(args)
						continue
					}
				}
			}
			// A names-only key suppresses only its own contribution to hide.
			// An inherited hide still wins: a names-only field nested under
			// something already hidden stays hidden.
			v[key] = walk(item, hide || (SensitiveKey(key) && !NamesOnlyKey(key)), schema || strings.HasSuffix(key, "_schema"))
		}
	case []any:
		for i, item := range v {
			v[i] = walk(item, hide, schema)
		}
	}
	return value
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
