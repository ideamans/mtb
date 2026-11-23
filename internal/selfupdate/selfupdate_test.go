package selfupdate

import "testing"

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"0.2.0", "0.3.0", -1}, {"0.10.0", "0.9.9", 1}, {"1.0.0", "v1.0.0", 0}} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s,%s)=%d", c.a, c.b, got)
		}
	}
	for _, v := range []string{"dev", "0.2.0-7-gabc", "0.2", ""} {
		if Valid(v) {
			t.Errorf("Valid(%q)", v)
		}
	}
}

func TestDetectManager(t *testing.T) {
	for exe, want := range map[string]Manager{
		"/usr/local/bin/mtb":                        ManagerNone,
		"/home/u/.local/bin/mtb":                    ManagerNone,
		"/usr/bin/mtb":                              ManagerSystem,
		"/usr/local/Cellar/mtb/0.2.0/bin/mtb":       ManagerHomebrew,
		"/home/linuxbrew/.linuxbrew/bin/mtb":        ManagerHomebrew,
		`C:\ProgramData\chocolatey\lib\mtb\mtb.exe`: ManagerChocolatey,
		`C:\Users\u\scoop\apps\mtb\current\mtb.exe`: ManagerScoop,
	} {
		if got := DetectManager(exe); got != want {
			t.Errorf("%s: %q", exe, got)
		}
	}
}

func TestLookupSum(t *testing.T) {
	sum := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got, err := lookupSum([]byte(sum+"  mtb_1.0.0_linux_amd64.tar.gz\n"), "mtb_1.0.0_linux_amd64.tar.gz")
	if err != nil || got != sum {
		t.Fatal(got, err)
	}
	if _, err := lookupSum([]byte(sum+"  other\n"), "mtb_1.0.0_linux_amd64.tar.gz"); err == nil {
		t.Fatal("見つからないのに通った")
	}
}
