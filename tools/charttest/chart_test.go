// Package charttest verifies the deployment contract using Helm's real renderer
// and the same configuration loader that runs in the container.
package charttest

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"gopkg.in/yaml.v3"
)

const chart = "../../charts/qiotlctl"

type doc = map[string]any

func render(t *testing.T, values string, flags ...string) ([]doc, string, error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm is required for chart tests")
	}
	args := []string{"template", "fixture", chart, "--namespace", "sensors"}
	if values != "" {
		p := filepath.Join(t.TempDir(), "values.yaml")
		if err := os.WriteFile(p, []byte(values), 0600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-f", p)
	}
	args = append(args, flags...)
	b, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		return nil, string(b), err
	}
	var docs []doc
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var d doc
		if err := decoder.Decode(&d); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("invalid or duplicate YAML: %v", err)
		}
		if len(d) > 0 {
			docs = append(docs, d)
		}
	}
	return docs, string(b), nil
}
func object(t *testing.T, docs []doc, kind string) doc {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == kind {
			return d
		}
	}
	t.Fatalf("missing %s", kind)
	return nil
}
func at(m map[string]any, path ...string) any {
	var v any = m
	for _, k := range path {
		v = v.(map[string]any)[k]
	}
	return v
}
func TestDefaultContract(t *testing.T) {
	docs, first, err := render(t, "")
	if err != nil {
		t.Fatal(first, err)
	}
	_, second, err := render(t, "")
	if err != nil || first != second {
		t.Fatal("render must be deterministic")
	}
	d := object(t, docs, "Deployment")
	if at(d, "spec", "replicas") != 1 || at(d, "spec", "strategy", "type") != "Recreate" {
		t.Fatal("single daemon upgrade contract")
	}
	if at(d, "spec", "template", "spec", "automountServiceAccountToken") != false {
		t.Fatal("API token mounted")
	}
	if at(object(t, docs, "Service"), "spec", "publishNotReadyAddresses") != true {
		t.Fatal("metrics unavailable during readiness failures")
	}
	if strings.Contains(first, "QIOTLCTL_VM_PASSWORD") {
		t.Fatal("disabled VM requires credentials")
	}
	if at(object(t, docs, "PersistentVolumeClaim"), "metadata", "annotations", "helm.sh/resource-policy") != "keep" {
		t.Fatal("PVC retention missing")
	}
	if strings.Contains(first, "storageClassName:") {
		t.Fatal("default storage class must remain unspecified")
	}
}
func TestNativeConfigurationAndRawOverlays(t *testing.T) {
	docs, out, err := render(t, "", "-f", chart+"/examples/minimal.yaml", "-f", chart+"/examples/multiple-files.yaml")
	if err != nil {
		t.Fatal(out, err)
	}
	data := object(t, docs, "Secret")["stringData"].(map[string]any)
	dir := t.TempDir()
	for name, content := range data {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content.(string)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := config.Load([]string{filepath.Join(dir, "config.yaml"), filepath.Join(dir, "profiles.yaml"), filepath.Join(dir, "bedroom.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(true); err != nil {
		t.Fatal(err)
	}
	if c.Runtime.Mode != "observe" || c.Outputs.HomeAssistant.Enabled || c.Outputs.VictoriaMetrics.Enabled {
		t.Fatal("unexpected publishing")
	}
	// A raw null must remove the inherited field, while preserving upload_interval.
	b, err := yaml.Marshal(c.Devices["bedroom"].Settings)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("record_interval")) || !bytes.Contains(b, []byte("upload_interval")) {
		t.Fatalf("profile removal lost: %s", b)
	}
	if !strings.Contains(out, "/etc/qiotld/bedroom.yaml") {
		t.Fatal("overlay not passed to daemon")
	}
}
func TestExternalResourcesAndEmptySelectors(t *testing.T) {
	values := `config:
  existingSecret: supplied
  files: {overlay.yaml: 'runtime: {mode: observe}'}
  overlayFiles: [overlay.yaml]
persistence: {existingClaim: saved-data}
mqttCredentials: {existingSecret: mqtt-auth}
vmCredentials: {existingSecret: vm-auth}
networkPolicy:
  allowDNS: false
  egressEndpoints:
    - namespaceSelector: {}
      podSelector: {matchLabels: {app: emqx}}
      ports: [{port: 1883}]
image: {digest: 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}
`
	docs, out, err := render(t, values)
	if err != nil {
		t.Fatal(out, err)
	}
	for _, d := range docs {
		if d["kind"] == "PersistentVolumeClaim" {
			t.Fatal("created PVC alongside existing claim")
		}
	}
	for _, needle := range []string{"claimName: saved-data", "namespaceSelector: {}", "name: supplied", "QIOTLCTL_VM_PASSWORD", "@sha256:"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("missing %s", needle)
		}
	}
	if _, ok := object(t, docs, "Secret")["stringData"].(map[string]any)["config.yaml"]; ok {
		t.Fatal("generated config despite external source")
	}
	_, out, err = render(t, "persistence: {storageClass: ''}\nnetworkPolicy: {allowDNS: false}")
	if err != nil || !strings.Contains(out, `storageClassName: ""`) || !strings.Contains(out, "egress: []") {
		t.Fatal(out, err)
	}
}
func TestOptionalIntegrations(t *testing.T) {
	docs, out, err := render(t, "", "-f", chart+"/examples/minimal.yaml", "-f", chart+"/examples/outputs.yaml", "-f", chart+"/examples/vmuser.yaml",
		"--api-versions", "operator.victoriametrics.com/v1beta1/VMUser", "--api-versions", "operator.victoriametrics.com/v1beta1/VMServiceScrape")
	if err != nil {
		t.Fatal(out, err)
	}
	v := object(t, docs, "VMUser")
	if at(v, "metadata", "namespace") != "sensors" {
		t.Fatal("VM credential namespace mismatch")
	}
	object(t, docs, "VMServiceScrape")
	if !strings.Contains(out, "name: vmuser-fixture-qiotlctl") {
		t.Fatal("operator secret missing")
	}
	docs, out, err = render(t, "monitoring: {kind: serviceMonitor}", "--api-versions", "monitoring.coreos.com/v1/ServiceMonitor")
	if err != nil {
		t.Fatal(out, err)
	}
	object(t, docs, "ServiceMonitor")
}
func TestInvalidValues(t *testing.T) {
	for _, values := range []string{
		"config: {existingSecret: a, existingConfigMap: b}",
		"persistence: {enabled: false}",
		"persistence: {enabled: false, allowEphemeral: true, existingClaim: old}",
		"persistence: {accessModes: [ReadWriteMany]}",
		"config: {broker: {host: old-python-value}}",
		"imagePullSecrets: [old-python-string]",
		"replicaCount: 2",
		"monitoring: {kind: serviceMonitor}",
		"vmUser: {enabled: true}",
		"vmUser: {enabled: true}\nvmCredentials: {existingSecret: conflict}",
		"config: {files: {config.yaml: duplicate}}",
		"podLabels: {app.kubernetes.io/name: broken-selector}",
		"networkPolicy: {egressEndpoints: [{ipBlock: {cidr: 127.0.0.1/32}, podSelector: {}, ports: [{port: 1883}]}]}",
		"persistence: {annotations: {helm.sh/resource-policy: delete}}",
		"mqttCredentials: {existingSecret: auth}\nextraEnv: [{name: QIOTLCTL_MQTT_PASSWORD, value: duplicate}]",
		"extraEnv: [{name: TEST, value: first}, {name: TEST, value: second}]",
		"config: {overlayFiles: ['../outside.yaml']}",
	} {
		t.Run(values, func(t *testing.T) {
			if _, _, err := render(t, values); err == nil {
				t.Fatal("invalid values accepted")
			}
		})
	}
}
func TestTLSAndEphemeralRendering(t *testing.T) {
	_, out, err := render(t, "persistence: {enabled: false, allowEphemeral: true}", "-f", chart+"/examples/tls.yaml")
	if err != nil {
		t.Fatal(out, err)
	}
	if strings.Contains(out, "kind: PersistentVolumeClaim") || !strings.Contains(out, "name: broker-tls") {
		t.Fatal("incorrect volumes")
	}
}
