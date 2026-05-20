package render

import (
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

func TestRenderMonitoringCompose_Basic(t *testing.T) {
	i := &intent.Intent{
		Name: "test",
		Monitoring: &intent.Monitoring{
			Enabled: intent.BoolPtr(true),
			Prometheus: intent.PromConfig{Port: 9090, Retention: "7d"},
			Grafana:    intent.GrafConfig{Port: 3000},
		},
	}
	targets := []MonitoringTarget{{Name: "test", Address: "test:9527"}}
	out := RenderMonitoringCompose("test", i, targets, "test_default")

	if !strings.Contains(out, "prometheus:") {
		t.Error("missing prometheus service")
	}
	if !strings.Contains(out, "grafana:") {
		t.Error("missing grafana service")
	}
	if !strings.Contains(out, "9090:9090") {
		t.Error("missing prometheus port mapping")
	}
	if !strings.Contains(out, "3000:3000") {
		t.Error("missing grafana port mapping")
	}
	if !strings.Contains(out, "test_default:") {
		t.Error("missing external network reference")
	}
	if !strings.Contains(out, "test-prometheus") {
		t.Error("missing prometheus container name")
	}
	if !strings.Contains(out, "v3.10.0") {
		t.Error("missing prometheus image version")
	}
}

func TestRenderMonitoringCompose_NoNetwork(t *testing.T) {
	i := &intent.Intent{
		Name: "test",
		Monitoring: &intent.Monitoring{
			Enabled: intent.BoolPtr(true),
			Prometheus: intent.PromConfig{Port: 9090, Retention: "7d"},
			Grafana:    intent.GrafConfig{Port: 3000},
		},
	}
	out := RenderMonitoringCompose("test", i, nil, "")
	if strings.Contains(out, "networks:") {
		t.Error("should not contain networks when networkName is empty")
	}
}

func TestRenderPrometheusConfig(t *testing.T) {
	targets := []MonitoringTarget{
		{Name: "node1", Address: "node1:9527", Labels: map[string]string{"instance": "node1"}},
	}
	out := RenderPrometheusConfig(targets, "7d")
	if !strings.Contains(out, "job_name: java-tron") {
		t.Error("missing job name")
	}
	if !strings.Contains(out, "node1:9527") {
		t.Error("missing target address")
	}
	if !strings.Contains(out, "instance: node1") {
		t.Error("missing label")
	}
}

func TestRenderGrafanaProvisioning(t *testing.T) {
	ds, prov := RenderGrafanaProvisioning("http://prometheus:9090")
	if !strings.Contains(ds, "prometheus") {
		t.Error("datasource missing prometheus name")
	}
	if !strings.Contains(ds, "http://prometheus:9090") {
		t.Error("datasource missing prometheus url")
	}
	if !strings.Contains(prov, "java-tron") {
		t.Error("provider missing name")
	}
}

func TestLoadDashboard(t *testing.T) {
	for _, name := range DashboardNames() {
		data, err := LoadDashboard(name)
		if err != nil {
			t.Errorf("LoadDashboard(%q): %v", name, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("LoadDashboard(%q): empty data", name)
		}
	}
}

func TestNormalizeDashboard(t *testing.T) {
	input := []byte(`{"panels":[{"datasource":{"uid":"old-uid"}}]}`)
	out := NormalizeDashboard(input)
	if !strings.Contains(string(out), `"uid":"prometheus"`) {
		t.Errorf("expected uid replaced with prometheus, got: %s", string(out))
	}
}
