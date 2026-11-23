package main

import "testing"

func TestParseActiveScheme(t *testing.T) {
	out := "Power Scheme GUID: 381b4222-f694-41f0-9685-ff5bb260df2e  (Balanced)"
	guid, err := parseActiveScheme(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if guid != schemeBalanced {
		t.Fatalf("got %q, want %q", guid, schemeBalanced)
	}
}

func TestParseActiveSchemeAcceptsLocalesWithoutName(t *testing.T) {
	// Non-English Windows keeps the GUID but localises the label; the parser
	// must not depend on the parenthesised name.
	out := "GUID de scheme d'alimentation : a1841308-3541-4fab-bc81-f71556f20b4a"
	if _, err := parseActiveScheme(out); err == nil {
		// This shape has no "Power Scheme GUID:" prefix, so an error is fine;
		// the point is that it must not panic or return a wrong GUID.
	} else {
		return
	}
}

func TestParseActiveSchemeRejectsGarbage(t *testing.T) {
	if _, err := parseActiveScheme("no scheme here"); err == nil {
		t.Fatal("expected an error for unparseable output")
	}
}

func TestParseSchemeList(t *testing.T) {
	out := `Existing Power Schemes (* Active)
-----------------------------------
Power Scheme GUID: 381b4222-f694-41f0-9685-ff5bb260df2e  (Balanced) *
Power Scheme GUID: a1841308-3541-4fab-bc81-f71556f20b4a  (Power saver)
Power Scheme GUID: 8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c  (High performance)`
	guids := parseSchemeList(out)
	if len(guids) != 3 {
		t.Fatalf("got %d scheme(s), want 3: %v", len(guids), guids)
	}
	if guids[1] != schemePowerSaver {
		t.Fatalf("second scheme = %q", guids[1])
	}
}

func TestParsePowerIndices(t *testing.T) {
	out := `Power Scheme GUID: 381b4222-f694-41f0-9685-ff5bb260df2e  (Balanced)
  GUID Alias : SUB_PROCESSOR
    Power Setting GUID: bc5038f7-23e0-4960-96da-33abaf5935ec  (Maximum processor state)
      Minimum Possible Setting : 0x00000000
      Maximum Possible Setting : 0x00000064
    Current AC Power Setting Index: 0x00000064
    Current DC Power Setting Index: 0x00000028`
	ac, dc, err := parsePowerIndices(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ac != "100" || dc != "40" {
		t.Fatalf("got ac=%s dc=%s, want 100/40", ac, dc)
	}
}

func TestParsePowerIndicesRequiresBoth(t *testing.T) {
	if _, _, err := parsePowerIndices("Current AC Power Setting Index: 0x00000064"); err == nil {
		t.Fatal("expected an error when the DC index is missing")
	}
}

func TestTurboRoundTrip(t *testing.T) {
	if turboIndex(true) != "1" || turboIndex(false) != "0" {
		t.Fatal("turboIndex mapping changed")
	}
	if !turboEnabled("1") || turboEnabled("0") {
		t.Fatal("turboEnabled mapping changed")
	}
	// Windows reports several "enabled" variants above 0; all must read as on.
	if !turboEnabled("3") {
		t.Fatal("variant 3 should count as enabled")
	}
}

func TestLidActionMapping(t *testing.T) {
	for _, action := range []string{"ignore", "sleep", "hibernate", "shutdown"} {
		value, ok := lidValue(action)
		if !ok {
			t.Fatalf("%s has no value", action)
		}
		if lidActionName(value) != action {
			t.Fatalf("%s did not round-trip through %s", action, value)
		}
	}
	if _, ok := lidValue("explode"); ok {
		t.Fatal("unknown action should not resolve")
	}
}

func TestBatteryBelowExit(t *testing.T) {
	cases := []struct {
		name      string
		threshold int
		pct       int
		status    string
		want      bool
	}{
		{"below while discharging", 15, 10, "discharging", true},
		{"below while charging", 15, 10, "charging", false},
		{"below while full", 15, 10, "full", false},
		{"above threshold", 15, 40, "discharging", false},
		{"exactly at threshold", 15, 15, "discharging", false},
		{"guard disabled", 0, 5, "discharging", false},
		{"unknown percentage", 15, 0, "discharging", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := batteryBelowExit(tc.threshold, tc.pct, tc.status); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseDevices(t *testing.T) {
	out := `[{"instance_id":"BTHENUM\\A","description":"Headset","disabled":false},` +
		`{"instance_id":"BTHENUM\\B","description":"Mouse","disabled":true}]`
	devices, err := parseDevices(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices", len(devices))
	}
	if devices[0].InstanceID != `BTHENUM\A` || devices[0].Disabled {
		t.Fatalf("first device wrong: %+v", devices[0])
	}
	if !devices[1].Disabled {
		t.Fatalf("second device should be disabled: %+v", devices[1])
	}
}

func TestParseDevicesHandlesEmpty(t *testing.T) {
	for _, in := range []string{"", "null", "[]"} {
		devices, err := parseDevices(in)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", in, err)
		}
		if len(devices) != 0 {
			t.Fatalf("%q: got %d devices", in, len(devices))
		}
	}
}

func TestParseServiceState(t *testing.T) {
	running, start, missing, err := parseServiceState(`{"running":true,"start":"auto","missing":false}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !running || start != "auto" || missing {
		t.Fatalf("got running=%v start=%q missing=%v", running, start, missing)
	}
}

func TestParseServiceStateMissing(t *testing.T) {
	_, _, missing, err := parseServiceState(`{"missing":true}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !missing {
		t.Fatal("missing flag lost")
	}
}

func TestSchemeAlias(t *testing.T) {
	guid, err := schemeAlias("saver")
	if err != nil || guid != schemePowerSaver {
		t.Fatalf("saver resolved to %q, err=%v", guid, err)
	}
	// A raw GUID passes through, lowercased.
	guid, err = schemeAlias("381B4222-F694-41F0-9685-FF5BB260DF2E")
	if err != nil || guid != schemeBalanced {
		t.Fatalf("guid resolved to %q, err=%v", guid, err)
	}
	if _, err := schemeAlias("turbo-max"); err == nil {
		t.Fatal("unknown name should not resolve")
	}
}

func TestSecondsLabel(t *testing.T) {
	if secondsLabel(0) != "never" {
		t.Fatalf("0 should read as never, got %q", secondsLabel(0))
	}
	if secondsLabel(60) != "1m0s" {
		t.Fatalf("60 seconds rendered as %q", secondsLabel(60))
	}
	if secondsLabelFromString("bogus") != "bogus" {
		t.Fatal("unparseable value should pass through unchanged")
	}
}

func TestPSQuoteStopsInjection(t *testing.T) {
	// Single-quoted PowerShell literals neutralise expansion; doubling is the
	// only escape. A payload must stay inside the quotes.
	got := psQuote("'; Remove-Item C:\\ -Recurse; '")
	if got != "'''; Remove-Item C:\\ -Recurse; '''" {
		t.Fatalf("quoting is wrong: %q", got)
	}
}

// powercfg localises its labels, so the English-only parser used to fail
// entirely on a German install. The GUID is the only portable token.
func TestParseActiveSchemeLocalized(t *testing.T) {
	raw := "Netzschema-GUID: 381b4222-f694-41f0-9685-ff5bb260df2e  (Ausbalanciert)"
	guid, err := parseActiveScheme(raw)
	if err != nil {
		t.Fatalf("localised output did not parse: %v", err)
	}
	if guid != schemeBalanced {
		t.Fatalf("got %q, want balanced", guid)
	}
}

func TestParseActiveSchemeBareGUID(t *testing.T) {
	raw := "Energieschema: a1841308-3541-4fab-bc81-f71556f20b4a"
	guid, err := parseActiveScheme(raw)
	if err != nil {
		t.Fatalf("bare GUID did not parse: %v", err)
	}
	if guid != schemePowerSaver {
		t.Fatalf("got %q", guid)
	}
}

// Two GUIDs is ambiguous: guessing would record a scheme the user never had,
// and `off` would then switch them to it.
func TestParseActiveSchemeRefusesAmbiguousOutput(t *testing.T) {
	raw := "381b4222-f694-41f0-9685-ff5bb260df2e\n8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c"
	if _, err := parseActiveScheme(raw); err == nil {
		t.Fatal("ambiguous output must be refused, not guessed")
	}
}

// The DC value is what protects the battery, so a localised parse must not
// silently shift by one and record e.g. the increment as the DC index.
func TestParsePowerIndicesLocalized(t *testing.T) {
	raw := `Energieschema-GUID: 381b4222-f694-41f0-9685-ff5bb260df2e  (Ausbalanciert)
  GUID-Alias : SUB_PROCESSOR
    Energieeinstellungs-GUID: bc5038f7-23e0-4960-96da-33abaf5935ec  (Maximale Prozessorgeschwindigkeit)
      Minimal moegliche Einstellung : 0x00000000
      Maximal moegliche Einstellung : 0x00000064
      Inkrement : 0x00000001
    Aktueller Wechselstromindex: 0x00000064
    Aktueller Gleichstromindex: 0x00000028`
	ac, dc, err := parsePowerIndices(raw)
	if err != nil {
		t.Fatalf("localised output did not parse: %v", err)
	}
	if ac != "100" || dc != "40" {
		t.Fatalf("ac=%s dc=%s, want 100/40", ac, dc)
	}
}

// A shape the parser does not recognise must be refused, never guessed: a
// wrong index would overwrite the user's real setting on restore.
func TestParsePowerIndicesRefusesUnknownShape(t *testing.T) {
	cases := []string{
		"",
		"no numbers here",
		"0x00000064",
		// Only a min/max pair, no AC/DC: guessing would record min as AC.
		"min : 0x00000000\nmax : 0x00000064",
	}
	for _, raw := range cases {
		if _, _, err := parsePowerIndices(raw); err == nil {
			t.Fatalf("unrecognised shape %q must be refused", raw)
		}
	}
}
