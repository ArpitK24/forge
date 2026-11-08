package symbols

import (
	"strings"
	"unicode"
)

// Terms tokenizes text for lexical retrieval. Every maximal run of
// letters, digits and underscores is an identifier; each identifier
// contributes its lowercase sub-words (snake_case and camelCase split,
// acronyms kept together: "parseHTTPRequest" -> parse, http, request)
// plus the whole lowercased identifier when it was compound. Terms
// shorter than two characters, pure numbers and stopwords are dropped.
// The order of the result follows the input, so callers can count
// frequencies deterministically.
func Terms(text string) []string {
	var out []string
	emit := func(w string) {
		if len(w) < 2 || isNumber(w) || stopwords[w] {
			return
		}
		out = append(out, w)
	}
	for _, id := range Identifiers(text) {
		parts := SplitIdent(id)
		for _, p := range parts {
			emit(p)
		}
		if len(parts) > 1 {
			emit(strings.ToLower(strings.Trim(id, "_")))
		}
	}
	return out
}

// Identifiers returns the maximal [A-Za-z0-9_] runs in text (Unicode
// letters and digits included), in order.
func Identifiers(text string) []string {
	var out []string
	start := -1
	for i, r := range text {
		ok := r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
		if ok && start < 0 {
			start = i
		} else if !ok && start >= 0 {
			out = append(out, text[start:i])
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, text[start:])
	}
	return out
}

// SplitIdent splits an identifier on underscores and case changes and
// lowercases the parts: "HTTPServer_closeAll2" -> http, server, close,
// all2.
func SplitIdent(id string) []string {
	var parts []string
	for _, chunk := range strings.Split(id, "_") {
		if chunk == "" {
			continue
		}
		rs := []rune(chunk)
		start := 0
		for i := 1; i < len(rs); i++ {
			prev, cur := rs[i-1], rs[i]
			boundary := false
			switch {
			case unicode.IsLower(prev) && unicode.IsUpper(cur):
				boundary = true
			case unicode.IsDigit(prev) && unicode.IsUpper(cur):
				boundary = true
			case unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
				boundary = true // "HTTPServer": split before "S"
			}
			if boundary {
				parts = append(parts, strings.ToLower(string(rs[start:i])))
				start = i
			}
		}
		parts = append(parts, strings.ToLower(string(rs[start:])))
	}
	return parts
}

func isNumber(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// stopwords are English function words and the most common keywords of
// the languages we index; they carry no retrieval signal.
var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		a an and are as at be but by can could did do does for from had has have
		how if in into is it its it's may me might must my no not of on or our
		should so than that the their them then there these they this those to
		too was we were what when where which while who why will with would you
		your yes also just like use used using get set new one two all any some
		def class self return import none true false elif else pass raise try
		except finally lambda yield func var const type struct interface package
		nil err go defer chan map range break continue switch case default`) {
		stopwords[w] = true
	}
}
