package main

import (
	"strings"
	"testing"
)

// windowsParseArgs splits a Windows command line using the documented CRT /
// CommandLineToArgvW rules. It exists to test quoteWindowsArg from the reader
// side: if the writer is wrong anywhere, the reader will disagree. Keeping an
// independent implementation is the point -- reusing the writer's own logic
// would prove nothing.
func windowsParseArgs(cmdline string) []string {
	var args []string
	i, n := 0, len(cmdline)
	for {
		for i < n && (cmdline[i] == ' ' || cmdline[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}
		var cur []byte
		inQuotes := false
		for i < n {
			c := cmdline[i]
			switch {
			case c == '\\':
				j := i
				for j < n && cmdline[j] == '\\' {
					j++
				}
				count := j - i
				if j < n && cmdline[j] == '"' {
					// 2n backslashes then a quote: n backslashes, quote is syntax.
					// 2n+1: n backslashes, quote is literal.
					cur = append(cur, bytesRepeat('\\', count/2)...)
					if count%2 == 1 {
						cur = append(cur, '"')
					} else {
						inQuotes = !inQuotes
					}
					i = j + 1
				} else {
					cur = append(cur, bytesRepeat('\\', count)...)
					i = j
				}
			case c == '"':
				inQuotes = !inQuotes
				i++
			case (c == ' ' || c == '\t') && !inQuotes:
				goto done
			default:
				cur = append(cur, c)
				i++
			}
		}
	done:
		args = append(args, string(cur))
		for i < n && (cmdline[i] == ' ' || cmdline[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}
	}
	return args
}

func bytesRepeat(b byte, count int) []byte {
	if count <= 0 {
		return nil
	}
	return []byte(strings.Repeat(string(b), count))
}

// TestQuoteWindowsArgRoundTrips is a property test: every awkward argument must
// survive emit-then-parse unchanged. This covers the cases that actually break
// in practice -- a username with a space, a trailing backslash, an embedded
// quote -- plus exhaustive short strings over the characters the rules treat
// specially.
func TestQuoteWindowsArgRoundTrips(t *testing.T) {
	cases := []string{
		"",
		"a",
		" ",
		`\`,
		`\\`,
		`"`,
		`\"`,
		`a b`,
		`a\tb`,
		"\n",
		`C:\Users\First Last\AppData\Local\Temp\x.log`,
		`C:\Program Files\app.exe`,
		`C:\dir with space\`,
		`\\server\share\file name.txt`,
		`trailing\`,
		`say "hi"`,
		`"already"`,
		`a"b\"c`,
		`é naïve`,
	}

	// Exhaustive short strings over the characters the splitting rules single
	// out. Length 3 is 27 cases; length 4 would be 81, still cheap.
	alphabet := []string{"a", " ", `\`, `"`}
	var build func(prefix string, depth int)
	build = func(prefix string, depth int) {
		if depth == 0 {
			cases = append(cases, prefix)
			return
		}
		for _, ch := range alphabet {
			build(prefix+ch, depth-1)
		}
	}
	build("", 4)

	for _, want := range cases {
		quoted := quoteWindowsArg(want)
		got := windowsParseArgs(quoted)
		if len(got) != 1 {
			t.Fatalf("arg %q quoted to %q parsed into %d tokens: %q", want, quoted, len(got), got)
		}
		if got[0] != want {
			t.Fatalf("round trip changed the value:\n   in=%q\nquoted=%q\n  out=%q", want, quoted, got[0])
		}
	}
}

// Multiple arguments must stay separate and in order.
func TestQuoteWindowsArgsRoundTripsMany(t *testing.T) {
	sets := [][]string{
		{"on"},
		{"--elevated", "on", "--log-file", `C:\Users\First Last\AppData\Local\Temp\x.log`},
		{`a b`, `c d`, `e`},
		{``, ``, `x`},
		{`C:\dir with space\`, `next`},
		{`--dry-run`, `off`},
	}
	for _, want := range sets {
		cmdline := quoteWindowsArgs(want)
		got := windowsParseArgs(cmdline)
		if len(got) != len(want) {
			t.Fatalf("args %q merged or split: %q -> %q", want, cmdline, got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("arg %d changed: %q -> %q (cmdline %q)", i, want[i], got[i], cmdline)
			}
		}
	}
}
