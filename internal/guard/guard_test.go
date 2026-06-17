package guard

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

// setGate sets the flag + env for one test and restores both after.
func setGate(t *testing.T, flag bool, env string) {
	t.Helper()
	oldFlag := FlagValue
	oldEnv, hadEnv := os.LookupEnv(EnvVar)
	FlagValue = flag
	if env == "" {
		_ = os.Unsetenv(EnvVar)
	} else {
		_ = os.Setenv(EnvVar, env)
	}
	t.Cleanup(func() {
		FlagValue = oldFlag
		if hadEnv {
			_ = os.Setenv(EnvVar, oldEnv)
		} else {
			_ = os.Unsetenv(EnvVar)
		}
	})
}

func TestRequested_Floor(t *testing.T) {
	cases := []struct {
		flag bool
		env  string
		want bool
	}{
		{false, "", false},
		{true, "", true},
		{false, "1", true},
		{false, "true", true},
		{false, "yes", true},
		{false, "on", true},
		{false, "0", false},
		{false, "false", false},
		{false, "nonsense", false},
		{true, "0", true}, // flag on, env off → still on (OR)
	}
	for _, tc := range cases {
		setGate(t, tc.flag, tc.env)
		if got := Requested(); got != tc.want {
			t.Errorf("Requested(flag=%v env=%q) = %v, want %v", tc.flag, tc.env, got, tc.want)
		}
	}
}

func TestEnvTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "Yes", "on", " t "} {
		if !envTruthy(v) {
			t.Errorf("envTruthy(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "x"} {
		if envTruthy(v) {
			t.Errorf("envTruthy(%q) = true, want false", v)
		}
	}
}

func wantPrivReq(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected PRIVATE_NETWORK_REQUIRED, got nil")
	}
	var se *output.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("expected *output.StructuredError, got %T: %v", err, err)
	}
	if se.Code != "PRIVATE_NETWORK_REQUIRED" || se.ExitCode != output.ExitValidationError {
		t.Errorf("got code=%q exit=%d; want PRIVATE_NETWORK_REQUIRED/%d", se.Code, se.ExitCode, output.ExitValidationError)
	}
}

func TestEnforce(t *testing.T) {
	// Gate off → always allow, even mainnet.
	setGate(t, false, "")
	if err := Enforce("mainnet"); err != nil {
		t.Errorf("gate off: Enforce(mainnet) = %v, want nil", err)
	}

	// Gate on.
	setGate(t, true, "")
	if err := Enforce("private"); err != nil {
		t.Errorf("Enforce(private) = %v, want nil", err)
	}
	wantPrivReq(t, Enforce("mainnet"))
	wantPrivReq(t, Enforce("nile"))

	// Empty network (legacy node) refuses with a re-apply suggestion.
	err := Enforce("")
	wantPrivReq(t, err)
	var se *output.StructuredError
	_ = errors.As(err, &se)
	foundReapply := false
	for _, s := range se.Suggestions {
		if strings.Contains(strings.ToLower(s), "re-apply") {
			foundReapply = true
		}
	}
	if !foundReapply {
		t.Errorf("empty-network error should suggest re-apply; got %v", se.Suggestions)
	}
}

func TestEnforceArg_ForcesWhenGlobalOff(t *testing.T) {
	setGate(t, false, "") // global gate OFF
	// requested=true forces enforcement for this call.
	wantPrivReq(t, EnforceArg(true, "mainnet"))
	if err := EnforceArg(true, "private"); err != nil {
		t.Errorf("EnforceArg(true, private) = %v, want nil", err)
	}
	// requested=false + global off → allow.
	if err := EnforceArg(false, "mainnet"); err != nil {
		t.Errorf("EnforceArg(false, mainnet) with gate off = %v, want nil", err)
	}
}
