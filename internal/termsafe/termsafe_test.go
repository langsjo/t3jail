package termsafe

import "testing"

func TestEscape(t *testing.T) {
	cases := map[string]string{
		"plain text\n":            "plain text\n",
		"line\r\n":                "line\n",
		"\x1b[2J\x1b]0;title\x07": "^[[2J^[]0;title^G",
		"tab\there":               "tab\there",
		"del\x7f":                 "del^?",
		"ünïcödé ✓":               "ünïcödé ✓",
		"bad\xffbyte":             "bad\\xffbyte",
		"c1 csi \u009b31m":        "c1 csi M-^[31m",
		"lone cr\rovertyped":      "lone cr^Movertyped",
	}
	for in, want := range cases {
		if got := string(Escape([]byte(in))); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"plain\ttext\n":             "plain\ttext\n",
		"\x1b]52;c;aGk=\x07\x1b[2J": "?]52;c;aGk=??[2J",
		"ünïcödé ✓":                 "ünïcödé ✓",
		"bad\xffbyte":               "bad?byte",
		"c1 \u009b2J":               "c1 ??2J",
		"cr\r nul\x00":              "cr\r nul?",
	}
	for in, want := range cases {
		b := []byte(in)
		Mask(b, "\r")
		if string(b) != want {
			t.Errorf("Mask(%q) = %q, want %q", in, b, want)
		}
	}
}
