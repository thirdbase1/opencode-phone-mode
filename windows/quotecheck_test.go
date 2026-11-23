package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPSQuoteIsInjectionSafe proves the quoting claim with a real PowerShell
// parser instead of reasoning about it. The quoted literal is handed straight
// to ParseInput, so the AST describes the literal itself, not some value that
// a second parse produced. It must contain no commands, no variables, and no
// subexpressions, exactly one single-quoted constant, and resolve back to the
// original text.
func TestPSQuoteIsInjectionSafe(t *testing.T) {
	pwsh := os.Getenv("PWSH")
	if pwsh == "" {
		t.Skip("PWSH not set; skipping real-parser check")
	}
	hostile := []string{
		`'; Remove-Item C:\ -Recurse; '`,
		`$(Get-Process)`,
		"`whoami`",
		`"quoted"`,
		`'`,
		`''`,
		`''''`,
		`C:\Program Files\weird name\app.exe`,
		`&{calc}`,
		"a\nb",
		`;exit 1;#`,
		`$env:PATH`,
		`--dry-run'; Start-Process calc; '`,
	}
	for _, in := range hostile {
		// The literal is embedded as the direct argument to ParseInput. No
		// intermediate variable, so nothing is re-parsed or re-quoted.
		script := strings.Join([]string{
			`$src = @'` + "\n" + psQuote(in) + "\n" + `'@`,
			`$errors = $null`,
			`$ast = [System.Management.Automation.Language.Parser]::ParseInput($src, [ref]$null, [ref]$errors)`,
			`if ($errors.Count -gt 0) { "PARSE ERRORS"; exit 1 }`,
			`$cmds = @($ast.FindAll({ $args[0] -is [System.Management.Automation.Language.CommandAst] }, $true))`,
			`if ($cmds.Count -ne 0) { "INJECTED COMMAND: " + $cmds[0].GetCommandName(); exit 2 }`,
			`$subs = @($ast.FindAll({ $args[0] -is [System.Management.Automation.Language.SubExpressionAst] }, $true))`,
			`if ($subs.Count -ne 0) { "INJECTED SUBEXPRESSION"; exit 3 }`,
			`$vars = @($ast.FindAll({ $args[0] -is [System.Management.Automation.Language.VariableExpressionAst] }, $true))`,
			`if ($vars.Count -ne 0) { "INJECTED VARIABLE"; exit 4 }`,
			`$strings = @($ast.FindAll({ $args[0] -is [System.Management.Automation.Language.StringConstantExpressionAst] }, $true))`,
			`if ($strings.Count -ne 1) { "EXPECTED ONE LITERAL, GOT " + $strings.Count + ": " + (($strings | ForEach-Object { $_.StringConstantType.ToString() + "=" + $_.Extent.Text }) -join " | "); exit 5 }`,
			`if ($strings[0].StringConstantType -ne 'SingleQuoted') { "NOT SINGLE QUOTED: " + $strings[0].StringConstantType; exit 6 }`,
			`$bytes = [System.Text.Encoding]::UTF8.GetBytes($strings[0].Value)`,
			`[Console]::Out.Write([Convert]::ToBase64String($bytes))`,
		}, "\n")

		// Written to a file so the argument is never re-quoted by a shell.
		path := filepath.Join(t.TempDir(), "check.ps1")
		if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
			t.Fatalf("write script: %v", err)
		}
		cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-File", path)
		cmd.Env = append(os.Environ(), "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("input %q was not safe: %v\nscript:\n%s\noutput:\n%s", in, err, script, out)
		}
		got, decErr := base64Decode(strings.TrimSpace(string(out)))
		if decErr != nil {
			t.Fatalf("decode output %q: %v", out, decErr)
		}
		if got != in {
			t.Fatalf("round trip changed the value:\n in=%q\nout=%q", in, got)
		}
	}
}

func base64Decode(s string) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var buf []byte
	val, bits := 0, 0
	for _, r := range s {
		idx := strings.IndexRune(alphabet, r)
		if idx < 0 {
			if r == '=' {
				break
			}
			return "", os.ErrInvalid
		}
		val = val<<6 | idx
		bits += 6
		if bits >= 8 {
			bits -= 8
			buf = append(buf, byte(val>>bits))
		}
	}
	return string(buf), nil
}
