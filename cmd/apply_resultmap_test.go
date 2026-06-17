package cmd

import (
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/apply"
)

// TestApplyResultMap_IncludesNetworkFact is the regression guard for the
// bug a struct-level test missed: apply -o json must actually carry
// `network` + `is_private`. The Result struct had them, but the inline
// resultMap dropped them — green struct test, broken wire output. This
// asserts the WIRE map, the layer that was wrong.
func TestApplyResultMap_IncludesNetworkFact(t *testing.T) {
	m := applyResultMap(&apply.Result{
		Name:      "n",
		Outcome:   "created",
		Network:   "private",
		IsPrivate: true,
		Endpoints: map[string]string{"http": "http://127.0.0.1:8090"},
	}, 42)

	if got, ok := m["network"]; !ok || got != "private" {
		t.Errorf("resultMap[network] = %v (present=%v); want private", got, ok)
	}
	got, ok := m["is_private"]
	if !ok {
		t.Fatal("resultMap missing is_private key")
	}
	if got != true {
		t.Errorf("resultMap[is_private] = %v; want true", got)
	}
}
