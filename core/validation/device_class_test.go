package validation

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

// deviceClassFixture returns a minimal valid device document as decoded YAML,
// with the class block customizable by the caller.
func deviceClassFixture(t *testing.T, class map[string]any) map[string]any {
	t.Helper()
	device := map[string]any{
		"id":           "device_0Q0MPJ4Z2ENZ97XWETRESKZGTH",
		"name":         "example-user-test",
		"os":           "linux",
		"architecture": "x86_64",
	}
	if class != nil {
		device["class"] = class
	}
	return map[string]any{
		"schema_version": 1,
		"device":         device,
		"workspace_roots": map[string]any{
			"control-plane": "${HOME}/Developer/control-plane",
		},
		"materialization": map[string]any{
			"default_mode": "absent",
			"include": []any{
				map[string]any{
					"selector":       "portfolio:estate-control-plane",
					"workspace_root": "control-plane",
					"mode":           "active",
				},
			},
		},
		"harnesses": []any{"codex"},
		"state":     map[string]any{"path": "${XDG_STATE_HOME}/github-device-sync"},
	}
}

func findCode(t *testing.T, findings []domain.Finding, code string) bool {
	t.Helper()
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func TestDeviceClassAcceptsDesktopGui(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	value := deviceClassFixture(t, map[string]any{
		"profile":          "desktop",
		"gui":              "enabled",
		"docker_mode":      "none",
		"execution_policy": "source-lsp-only",
	})
	if findings := set.Validate("device", value, "test"); len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

// TestDeviceClassAcceptsDesktopBuilds covers the exact class block the
// example-user-ubuntu-1 descriptor declares. No positive case existed for this
// profile, which is how the missing execution_policy mapping shipped.
func TestDeviceClassAcceptsDesktopBuilds(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	for _, gui := range []string{"enabled", "disabled"} {
		t.Run(gui, func(t *testing.T) {
			t.Parallel()
			value := deviceClassFixture(t, map[string]any{
				"profile":          "desktop-builds",
				"gui":              gui,
				"docker_mode":      "rootful",
				"execution_policy": "local-dev-with-builds",
			})
			if findings := set.Validate("device", value, "test"); len(findings) != 0 {
				t.Fatalf("expected no findings, got %#v", findings)
			}
		})
	}
}

func TestDeviceClassAcceptsServerHeadless(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	value := deviceClassFixture(t, map[string]any{
		"profile":          "server",
		"gui":              "disabled",
		"docker_mode":      "rootful",
		"execution_policy": "container-execution-only",
		"hardening":        map[string]any{"ssh": true, "ufw": true},
	})
	if findings := set.Validate("device", value, "test"); len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestDeviceClassAcceptsAbsent(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	value := deviceClassFixture(t, nil)
	if findings := set.Validate("device", value, "test"); len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestDeviceClassRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		osName   string
		class    map[string]any
		wantCode string
	}{
		{
			name: "server with gui enabled", osName: "linux",
			class:    map[string]any{"profile": "server", "gui": "enabled"},
			wantCode: "GDS_DEVICE_CLASS_SERVER_GUI",
		},
		{
			name: "desktop with docker", osName: "linux",
			class:    map[string]any{"profile": "desktop", "docker_mode": "rootful"},
			wantCode: "GDS_DEVICE_CLASS_DESKTOP_DOCKER",
		},
		{
			name: "macos server profile", osName: "macos",
			class:    map[string]any{"profile": "server"},
			wantCode: "GDS_DEVICE_CLASS_MACOS_CONFLICT",
		},
		{
			name: "macos docker rootful", osName: "macos",
			class:    map[string]any{"profile": "desktop", "docker_mode": "rootful"},
			wantCode: "GDS_DEVICE_CLASS_MACOS_CONFLICT",
		},
		{
			name: "desktop wrong execution policy", osName: "linux",
			class:    map[string]any{"profile": "desktop", "execution_policy": "container-execution-only"},
			wantCode: "GDS_DEVICE_CLASS_EXECUTION_POLICY",
		},
		{
			name: "desktop-builds wrong execution policy", osName: "linux",
			class: map[string]any{
				"profile":          "desktop-builds",
				"docker_mode":      "rootful",
				"execution_policy": "source-lsp-only",
			},
			wantCode: "GDS_DEVICE_CLASS_EXECUTION_POLICY",
		},
		{
			name: "desktop-builds without rootful docker", osName: "linux",
			class:    map[string]any{"profile": "desktop-builds", "docker_mode": "rootless"},
			wantCode: "GDS_DEVICE_CLASS_DESKTOP_BUILDS_DOCKER",
		},
		{
			name: "desktop with hardening", osName: "linux",
			class: map[string]any{
				"profile":   "desktop",
				"hardening": map[string]any{"ssh": true},
			},
			wantCode: "GDS_DEVICE_CLASS_HARDENING_PROFILE",
		},
	}
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			value := deviceClassFixture(t, tc.class)
			device := value["device"].(map[string]any)
			device["os"] = tc.osName
			findings := set.Validate("device", value, "test")
			if !findCode(t, findings, tc.wantCode) {
				t.Fatalf("expected finding %s, got %#v", tc.wantCode, findings)
			}
		})
	}
}

// TestDeviceClassRoundTripsThroughYAML ensures the class block survives the
// strict serialization decoder (no anchors/duplicate keys) the way a real
// device descriptor would be loaded.
func TestDeviceClassRoundTripsThroughYAML(t *testing.T) {
	t.Parallel()
	yaml := []byte(`
schema_version: 1
device:
  id: device_0Q0MPJ4Z2ENZ97XWETRESKZGTH
  name: example-user-test
  os: linux
  architecture: x86_64
  class:
    profile: desktop
    gui: enabled
    docker_mode: none
    execution_policy: source-lsp-only
workspace_roots:
  control-plane: "${HOME}/Developer/control-plane"
materialization:
  default_mode: absent
  include:
    - selector: "portfolio:estate-control-plane"
      workspace_root: control-plane
      mode: active
harnesses: [codex]
state:
  path: "${XDG_STATE_HOME}/github-device-sync"
`)
	value, err := serialization.Decode("device.yaml", yaml)
	if err != nil {
		t.Fatal(err)
	}
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	if findings := set.Validate("device", value, "test"); len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}
