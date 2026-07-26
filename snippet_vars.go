package main

import "strings"

// expandSnippetVars replaces ${name} and ${name:default} placeholders in a
// snippet body with values from vars. Resolution order per placeholder:
//
//  1. vars[name] if present (even if empty - an explicit empty value is honoured)
//  2. the inline :default when the placeholder carries one
//  3. left verbatim (${...}) when neither is available
//
// The scan is a plain left-to-right pass over literal "${" ... "}" spans; there
// is no nesting or escaping. A "${" with no closing "}" is emitted verbatim.
// Values are ad-hoc user input, never vault material - this only ever runs on
// the user's own snippet against their own connect.
func expandSnippetVars(body string, vars map[string]string) string {
	if !strings.Contains(body, "${") {
		return body
	}
	var b strings.Builder
	for i := 0; i < len(body); {
		if body[i] == '$' && i+1 < len(body) && body[i+1] == '{' {
			end := strings.IndexByte(body[i+2:], '}')
			if end < 0 {
				// Unterminated placeholder: emit the rest verbatim.
				b.WriteString(body[i:])
				break
			}
			token := body[i+2 : i+2+end]
			name, def := token, ""
			hasDef := false
			if colon := strings.IndexByte(token, ':'); colon >= 0 {
				name = token[:colon]
				def = token[colon+1:]
				hasDef = true
			}
			if v, ok := vars[name]; ok {
				b.WriteString(v)
			} else if hasDef {
				b.WriteString(def)
			} else {
				// No value, no default: leave the placeholder untouched.
				b.WriteString(body[i : i+2+end+1])
			}
			i += 2 + end + 1
			continue
		}
		b.WriteByte(body[i])
		i++
	}
	return b.String()
}
