package i18n

import (
	"strings"
	"testing"
)

// Every language must have exactly the English keys with the same number of %s.
func TestLanguagesComplete(t *testing.T) {
	for lang, m := range messages {
		for k, en := range messages[Default] {
			v, ok := m[k]
			if !ok {
				t.Errorf("%s: missing %q", lang, k)
				continue
			}
			if strings.Count(v, "%s") != strings.Count(en, "%s") {
				t.Errorf("%s: placeholder mismatch in %q", lang, k)
			}
		}
		for k := range m {
			if _, ok := messages[Default][k]; !ok {
				t.Errorf("%s: unknown key %q", lang, k)
			}
		}
	}
}
