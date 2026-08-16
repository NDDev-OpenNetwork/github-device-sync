package main

import "testing"

func TestDecodeRuntimeDriverRequestUsesJSONContract(t *testing.T) {
	request, err := decodeRuntimeDriverRequest([]byte(`{
  "schema_version":1,
  "harness":"codex",
  "harness_version":"fixture",
  "model_label":"gpt-5.5",
  "execution_profile":"read-only",
  "tools":[],
  "environment":{"os":"darwin","architecture":"arm64","executable":"/tmp/codex","command":"codex"},
  "gds_executable":"/tmp/gds",
  "skill_profile":"core",
  "contract_version":"1.1.0",
  "profile_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "repository_root":"/tmp/repository",
  "evidence_directory":"/tmp/evidence",
  "profile_path":"/tmp/profile.yaml",
  "runtime_contract":"/tmp/runtime.yaml",
  "trigger_corpus":"/tmp/trigger.json",
  "output_corpus":"/tmp/output.json",
  "enforcement_corpus":"/tmp/enforcement.json",
  "evidence_schema":"/tmp/evidence.json"
}`))
	if err != nil || request.Harness != "codex" {
		t.Fatalf("request=%+v err=%v", request, err)
	}
}
