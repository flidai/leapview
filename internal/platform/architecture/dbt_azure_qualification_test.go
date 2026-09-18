package architecture

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDBTAzureQualificationKeepsThreeFailClosedIdentityScopes(t *testing.T) {
	root := repoRoot(t)
	workflowPath := filepath.Join(root, ".github/workflows/dbt-warehouse-boundary-azure-qualification.yml")
	workflow := readDBTBoundaryWorkflow(t, workflowPath)
	body, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{
		"workflow_dispatch:",
		"environment: dbt-warehouse-boundary-azure-qualification",
		"github.ref == format('refs/heads/{0}', github.event.repository.default_branch)",
		"github.ref_protected == true",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Azure qualification workflow is missing %q", required)
		}
	}
	for _, forbidden := range []string{"pull_request:", "push:", "schedule:"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Azure qualification workflow must remain protected-manual only: found %q", forbidden)
		}
	}
	if workflow.Permissions["contents"] != "read" {
		t.Fatalf("top-level contents permission = %q, want read", workflow.Permissions["contents"])
	}
	if _, ok := workflow.Permissions["id-token"]; ok {
		t.Fatal("Azure qualification must not grant OIDC at workflow scope")
	}

	preflight := requireDBTAzureQualificationJob(t, workflow, "identity-preflight")
	producer := requireDBTAzureQualificationJob(t, workflow, "producer-publication")
	source := requireDBTAzureQualificationJob(t, workflow, "source-read-boundary")
	ducklake := requireDBTAzureQualificationJob(t, workflow, "ducklake-write-boundary")
	lifecycle := requireDBTAzureQualificationJob(t, workflow, "consumer-failure-lifecycle")
	gate := requireDBTAzureQualificationJob(t, workflow, "qualification-gate")

	for name, job := range map[string]dbtBoundaryJob{
		"identity-preflight":         preflight,
		"producer-publication":       producer,
		"source-read-boundary":       source,
		"ducklake-write-boundary":    ducklake,
		"consumer-failure-lifecycle": lifecycle,
	} {
		assertDBTAzureQualificationTrustedJob(t, name, job)
	}
	for name, job := range map[string]dbtBoundaryJob{
		"producer-publication":    producer,
		"source-read-boundary":    source,
		"ducklake-write-boundary": ducklake,
	} {
		if job.Permissions["contents"] != "read" || job.Permissions["id-token"] != "write" {
			t.Errorf("%s permissions = %#v, want contents read and job-local id-token write", name, job.Permissions)
		}
	}
	for name, job := range map[string]dbtBoundaryJob{
		"identity-preflight":         preflight,
		"consumer-failure-lifecycle": lifecycle,
	} {
		if _, ok := job.Permissions["id-token"]; ok {
			t.Errorf("%s must not receive Azure OIDC authority", name)
		}
	}

	preflightStep := dbtBoundaryStepByName(t, preflight.Steps, "Validate distinct qualification identities and scopes")
	if !strings.Contains(preflightStep.Run, "azure-qualify.sh preflight") {
		t.Fatal("identity preflight does not use the maintained fail-closed qualifier")
	}
	producerInputStep := dbtBoundaryStepByName(t, producer.Steps, "Read the bounded producer inputs")
	for _, required := range []string{
		"az storage blob download \\",
		"--auth-mode login \\",
		"--container-name \"$AZURE_SOURCE_CONTAINER\" \\",
	} {
		if !strings.Contains(producerInputStep.Run, required) {
			t.Errorf("producer input step is missing executable multiline command fragment %q", required)
		}
	}
	if strings.Contains(producerInputStep.Run, "download +") {
		t.Fatal("producer input step contains a non-executable patch marker")
	}

	assertDBTAzureLogin(t, producer, "Authenticate producer-write identity", "${{ vars.DBT_PRODUCER_CLIENT_ID }}")
	assertDBTAzureLogin(t, source, "Authenticate Source-read identity", "${{ vars.DBT_QUALIFICATION_SOURCE_CLIENT_ID }}")
	assertDBTAzureLogin(t, ducklake, "Authenticate DuckLake-write identity", "${{ vars.DBT_QUALIFICATION_DUCKLAKE_CLIENT_ID }}")

	if !containsString(producer.Needs, "identity-preflight") {
		t.Fatalf("producer needs = %v, want identity-preflight", producer.Needs)
	}
	if producer.Outputs["publication_prefix"] != "${{ steps.publish.outputs.publication_prefix }}" ||
		producer.Outputs["partial_prefix"] != "${{ steps.publish.outputs.partial_prefix }}" {
		t.Fatalf("producer publication outputs = %#v", producer.Outputs)
	}
	for name, job := range map[string]dbtBoundaryJob{"source": source, "ducklake": ducklake} {
		if !containsString(job.Needs, "producer-publication") {
			t.Errorf("%s needs = %v, want producer-publication", name, job.Needs)
		}
		if job.Env["PUBLICATION_PREFIX"] != "${{ needs.producer-publication.outputs.publication_prefix }}" {
			t.Errorf("%s publication input = %q, want complete publication output", name, job.Env["PUBLICATION_PREFIX"])
		}
		for _, step := range job.Steps {
			if dbtBoundaryStepContains(step, "partial_prefix") {
				t.Errorf("%s step %q can observe the intentionally incomplete prefix", name, step.Name)
			}
		}
	}
	for _, required := range []string{"producer-publication", "source-read-boundary", "ducklake-write-boundary", "consumer-failure-lifecycle"} {
		if !containsString(gate.Needs, required) {
			t.Errorf("qualification gate needs = %v, missing %s", gate.Needs, required)
		}
	}
	gateStep := dbtBoundaryStepByName(t, gate.Steps, "Require every qualification boundary")
	for _, result := range []string{"PRODUCER_RESULT", "SOURCE_RESULT", "DUCKLAKE_RESULT", "LIFECYCLE_RESULT"} {
		if !strings.Contains(gateStep.Run, result) {
			t.Errorf("qualification gate does not fail closed on %s", result)
		}
	}

	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if dbtBoundaryStepContains(step, "manifest.json") || dbtBoundaryStepContains(step, "run_results.json") {
				t.Errorf("%s step %q treats dbt metadata as qualification authority", jobName, step.Name)
			}
			if dbtBoundaryStepContains(step, "${{ secrets.DBT_PRODUCER") || dbtBoundaryStepContains(step, "${{ secrets.LEAPVIEW_SOURCE") || dbtBoundaryStepContains(step, "${{ secrets.LEAPVIEW_DUCKLAKE") {
				t.Errorf("%s step %q uses a client credential instead of OIDC", jobName, step.Name)
			}
		}
	}
	for _, forbidden := range []string{"actions/upload-artifact", "toJSON(secrets)", "printenv", "env |", "set -x"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Azure qualification workflow contains credential disclosure surface %q", forbidden)
		}
	}
}

func assertDBTAzureQualificationTrustedJob(t *testing.T, name string, job dbtBoundaryJob) {
	t.Helper()
	guard := strings.Join(strings.Fields(job.If), " ")
	for _, required := range []string{
		"github.ref == format('refs/heads/{0}', github.event.repository.default_branch)",
		"github.ref_protected == true",
	} {
		if !strings.Contains(guard, required) {
			t.Errorf("%s trusted-ref guard is missing %q: %q", name, required, job.If)
		}
	}
	if strings.Contains(guard, "||") {
		t.Errorf("%s trusted-ref guard is not fail-closed: %q", name, job.If)
	}
	if job.Environment != "dbt-warehouse-boundary-azure-qualification" {
		t.Errorf("%s environment = %q, want dbt-warehouse-boundary-azure-qualification", name, job.Environment)
	}
}

func TestDBTAzureQualificationUsesImmutableChecksummedPublicationAndSafeDenyProbes(t *testing.T) {
	root := repoRoot(t)
	scriptPath := filepath.Join(root, "scripts/dbt-warehouse-boundary-azure-qualify.sh")
	body, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{
		"sha256sum",
		"--overwrite false",
		"verify_exact_file_set",
		"verify_download_checksum",
		"GITHUB_REPOSITORY",
		"GITHUB_RUN_ID",
		"GITHUB_RUN_ATTEMPT",
		"GITHUB_SHA",
		"DBT_QUALIFICATION_PROJECT_UID",
		"DBT_QUALIFICATION_ENVIRONMENT",
		"expect_storage_status 403 PUT",
		"expect_storage_status 403 DELETE",
		"If-Match: \"leapview-never-match\"",
		"producer-publication-checksum",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Azure qualifier is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"--account-key",
		"AZURE_STORAGE_KEY",
		"connection_string",
		"manifest.json",
		"run_results.json",
		"publication marker",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Azure qualifier contains forbidden authority %q", forbidden)
		}
	}
	if strings.Contains(text, "set -x") {
		t.Fatal("Azure qualifier must never enable credential-bearing shell tracing")
	}
}

func TestDBTAzureQualificationReusesExistingFailureAndRecoveryAuthority(t *testing.T) {
	root := repoRoot(t)
	workflow := readDBTBoundaryWorkflow(t, filepath.Join(root, ".github/workflows/dbt-warehouse-boundary-azure-qualification.yml"))
	lifecycle := requireDBTAzureQualificationJob(t, workflow, "consumer-failure-lifecycle")
	step := dbtBoundaryStepByName(t, lifecycle.Steps, "Prove consumer failures retain the active generation")
	for _, required := range []string{
		"TestProducerNeutralWarehouseBoundaryQualification",
		"task ci:prepare",
		"./internal/app \\",
		"-run '^TestProducerNeutralWarehouseBoundaryQualification$' \\",
	} {
		if !strings.Contains(step.Run, required) {
			t.Errorf("consumer lifecycle evidence is missing %q", required)
		}
	}
	if strings.Contains(step.Run, "./internal/app +") {
		t.Fatal("consumer lifecycle step contains a non-executable patch marker")
	}
	for _, forbidden := range []string{"az storage blob delete", "az storage container delete", "catalog mutation"} {
		if strings.Contains(step.Run, forbidden) {
			t.Errorf("consumer recovery bypasses lifecycle authority through %q", forbidden)
		}
	}
}

func TestDBTAzureQualificationPreflightRejectsIdentityAndScopeCollisions(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "scripts/dbt-warehouse-boundary-azure-qualify.sh")
	base := map[string]string{
		"DBT_PRODUCER_CLIENT_ID":                     "producer-client",
		"DBT_QUALIFICATION_SOURCE_CLIENT_ID":         "source-client",
		"DBT_QUALIFICATION_DUCKLAKE_CLIENT_ID":       "ducklake-client",
		"AZURE_STORAGE_ACCOUNT":                      "producerstore",
		"AZURE_SOURCE_CONTAINER":                     "producer-input",
		"AZURE_PUBLICATION_CONTAINER":                "producer-publication",
		"DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT": "leapviewstore",
		"DBT_QUALIFICATION_DUCKLAKE_CONTAINER":       "ducklake-state",
		"DBT_QUALIFICATION_PROJECT_UID":              "project:warehouse-boundary",
		"DBT_QUALIFICATION_ENVIRONMENT":              "qualification",
	}
	tests := []struct {
		name     string
		override map[string]string
		wantErr  bool
	}{
		{name: "distinct bounded identities"},
		{name: "producer and Source collision", override: map[string]string{"DBT_QUALIFICATION_SOURCE_CLIENT_ID": "producer-client"}, wantErr: true},
		{name: "Source and DuckLake collision", override: map[string]string{"DBT_QUALIFICATION_DUCKLAKE_CLIENT_ID": "source-client"}, wantErr: true},
		{name: "producer and DuckLake collision", override: map[string]string{"DBT_QUALIFICATION_DUCKLAKE_CLIENT_ID": "producer-client"}, wantErr: true},
		{name: "producer input and publication collision", override: map[string]string{"AZURE_SOURCE_CONTAINER": "producer-publication"}, wantErr: true},
		{name: "publication and DuckLake scope collision", override: map[string]string{"DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT": "producerstore", "DBT_QUALIFICATION_DUCKLAKE_CONTAINER": "producer-publication"}, wantErr: true},
		{name: "unsafe storage name", override: map[string]string{"AZURE_STORAGE_ACCOUNT": "producer store"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := make(map[string]string, len(base))
			for key, value := range base {
				values[key] = value
			}
			for key, value := range test.override {
				values[key] = value
			}
			command := exec.Command("bash", script, "preflight")
			command.Env = []string{"PATH=" + os.Getenv("PATH")}
			for key, value := range values {
				command.Env = append(command.Env, key+"="+value)
			}
			err := command.Run()
			if test.wantErr && err == nil {
				t.Fatal("preflight accepted an unsafe identity or storage scope")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("preflight rejected distinct bounded identities: %v", err)
			}
		})
	}
}

func TestDBTAzureQualificationRequiresRBACCodeAndCrossScopeWriteDenial(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "scripts/dbt-warehouse-boundary-azure-qualify.sh")
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	publish := strings.SplitN(string(body), "publish() {", 2)
	if len(publish) != 2 || !strings.Contains(strings.SplitN(publish[1], "source_read() {", 2)[0],
		"expect_storage_status 403 PUT \\\n    \"$DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT\"") {
		t.Fatal("producer qualification does not probe denied DuckLake writes")
	}

	dir := t.TempDir()
	parquet := filepath.Join(dir, "parquet")
	if err := os.WriteFile(parquet, []byte("bounded parquet fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"az": `#!/usr/bin/env bash
set -euo pipefail
case "$1 $2 $3" in
  "account get-access-token "*) printf 'test-token\n' ;;
  "storage blob list")
    printf '%s/dim_customers.parquet\n%s/fct_orders.parquet\n' "$PUBLICATION_PREFIX" "$PUBLICATION_PREFIX" ;;
  "storage blob download")
    while (($#)); do
      if [[ "$1" == "--file" ]]; then cp "$MOCK_PARQUET" "$2"; exit 0; fi
      shift
    done
    exit 2 ;;
  *) exit 2 ;;
esac
`,
		"curl": `#!/usr/bin/env bash
set -euo pipefail
headers='' method='' url='' conditional=no
while (($#)); do
  case "$1" in
    --dump-header) headers="$2"; shift ;;
    --request) method="$2"; shift ;;
    --header) [[ "$2" == 'If-Match: "leapview-never-match"' ]] && conditional=yes; shift ;;
    https://*) url="$1" ;;
  esac
  shift
done
printf 'HTTP/1.1 %s Mocked\r\nx-ms-error-code: %s\r\n\r\n' "$MOCK_STATUS" "$MOCK_CODE" >"$headers"
printf '%s|%s|%s\n' "$method" "$url" "$conditional" >>"$MOCK_LOG"
printf '%s' "$MOCK_STATUS"
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256([]byte("bounded parquet fixture"))
	expected := hex.EncodeToString(digest[:])
	for _, tc := range []struct {
		name, status, code string
		wantSuccess        bool
	}{
		{name: "RBAC denial", status: "403", code: "AuthorizationPermissionMismatch", wantSuccess: true},
		{name: "generic forbidden", status: "403", code: "AuthorizationFailure"},
		{name: "missing Azure code", status: "403"},
		{name: "precondition failure", status: "412", code: "ConditionNotMet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "requests")
			command := exec.Command("bash", script, "source-read")
			command.Env = []string{
				"PATH=" + dir + ":" + os.Getenv("PATH"),
				"MOCK_PARQUET=" + parquet, "MOCK_LOG=" + logPath,
				"MOCK_STATUS=" + tc.status, "MOCK_CODE=" + tc.code,
				"AZURE_STORAGE_ACCOUNT=producerstore", "AZURE_SOURCE_CONTAINER=producer-input",
				"AZURE_PUBLICATION_CONTAINER=producer-publication",
				"DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT=leapviewstore",
				"DBT_QUALIFICATION_DUCKLAKE_CONTAINER=ducklake-state",
				"PUBLICATION_PREFIX=qualification/run", "GITHUB_RUN_ID=123",
				"EXPECTED_DIM_CUSTOMERS_SHA256=" + expected,
				"EXPECTED_FCT_ORDERS_SHA256=" + expected,
			}
			output, err := command.CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("source-read error = %v, want success %v; output: %s", err, tc.wantSuccess, output)
			}
			if tc.wantSuccess {
				requests, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(requests), "PUT|https://leapviewstore.blob.core.windows.net/ducklake-state/qualification/source-deny-123-write|yes") {
					t.Fatalf("Source boundary did not make a safe denied DuckLake write probe: %s", requests)
				}
			}
		})
	}
}

func requireDBTAzureQualificationJob(t *testing.T, workflow dbtBoundaryWorkflow, name string) dbtBoundaryJob {
	t.Helper()
	job, ok := workflow.Jobs[name]
	if !ok {
		t.Fatalf("Azure qualification workflow is missing %s job", name)
	}
	return job
}

func assertDBTAzureLogin(t *testing.T, job dbtBoundaryJob, stepName, clientID string) {
	t.Helper()
	step := dbtBoundaryStepByName(t, job.Steps, stepName)
	if !strings.HasPrefix(step.Uses, "azure/login@") {
		t.Errorf("%s uses %q, want pinned azure/login", stepName, step.Uses)
	}
	for key, want := range map[string]string{
		"client-id":       clientID,
		"tenant-id":       "${{ vars.AZURE_TENANT_ID }}",
		"subscription-id": "${{ vars.AZURE_SUBSCRIPTION_ID }}",
	} {
		if got := toString(step.With[key]); got != want {
			t.Errorf("%s %s = %q, want %q", stepName, key, got, want)
		}
	}
}
