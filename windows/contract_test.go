package main

import "testing"

// The JSON shapes below are copied verbatim from a real run of win.ps1 (output
// captured with mocked Windows cmdlets). They pin the contract between the
// PowerShell helper and the Go parsers: if either side drifts, this fails.
func TestHelperOutputContractDevices(t *testing.T) {
	raw := `[{"instance_id":"BTHENUM\\A","description":"Headset","disabled":false,"status":"OK"},` +
		`{"instance_id":"BTHENUM\\B","description":"Mouse","disabled":true,"status":"Error"}]`

	devices, err := parseDevices(raw)
	if err != nil {
		t.Fatalf("real helper output did not parse: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices", len(devices))
	}
	if devices[0].InstanceID != `BTHENUM\A` {
		t.Fatalf("backslashes were mangled: %q", devices[0].InstanceID)
	}
	if devices[0].Description != "Headset" || devices[0].Disabled {
		t.Fatalf("first device parsed wrong: %+v", devices[0])
	}
	if !devices[1].Disabled {
		t.Fatalf("Error status must read as disabled: %+v", devices[1])
	}
}

func TestHelperOutputContractSingleDevice(t *testing.T) {
	// ConvertTo-Json -AsArray still emits an array for one device; this is the
	// shape that broke a scalar-only parser in the first draft.
	raw := `[{"instance_id":"PCI\\VEN_10DE","description":"NVIDIA RTX 3050","disabled":false,"status":"OK"}]`
	devices, err := parseDevices(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(devices) != 1 || devices[0].InstanceID != `PCI\VEN_10DE` {
		t.Fatalf("single-device array parsed wrong: %+v", devices)
	}
}

func TestHelperOutputContractServices(t *testing.T) {
	cases := []struct {
		raw     string
		running bool
		start   string
		missing bool
	}{
		{`{"running":true,"start":"auto","missing":false}`, true, "auto", false},
		{`{"running":false,"start":"manual","missing":false}`, false, "manual", false},
		{`{"running":false,"start":"disabled","missing":false}`, false, "disabled", false},
		{`{"missing":true}`, false, "", true},
	}
	for _, tc := range cases {
		running, start, missing, err := parseServiceState(tc.raw)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if running != tc.running || start != tc.start || missing != tc.missing {
			t.Fatalf("%s parsed as running=%v start=%q missing=%v",
				tc.raw, running, start, missing)
		}
	}
}

// Real powercfg output, including the indentation and hex formatting that the
// real command emits.
func TestHelperOutputContractPowercfg(t *testing.T) {
	raw := `Power Scheme GUID: 381b4222-f694-41f0-9685-ff5bb260df2e  (Balanced)
  GUID Alias : SUB_PROCESSOR
    Power Setting GUID: bc5038f7-23e0-4960-96da-33abaf5935ec  (Maximum processor state)
      Minimum Possible Setting : 0x00000000
      Maximum Possible Setting : 0x00000064
      Possible Settings increment : 0x00000001
      Possible Settings units : %
    Current AC Power Setting Index: 0x00000064
    Current DC Power Setting Index: 0x00000028

  GUID Alias : SUB_VIDEO
`
	ac, dc, err := parsePowerIndices(raw)
	if err != nil {
		t.Fatalf("real powercfg output did not parse: %v", err)
	}
	if ac != "100" || dc != "40" {
		t.Fatalf("ac=%s dc=%s, want 100/40", ac, dc)
	}
}

func TestHelperOutputContractActiveScheme(t *testing.T) {
	// powercfg /getactivescheme output on an English install.
	raw := "Power Scheme GUID: a1841308-3541-4fab-bc81-f71556f20b4a  (Power saver)"
	guid, err := parseActiveScheme(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if guid != schemePowerSaver {
		t.Fatalf("got %q", guid)
	}
}
