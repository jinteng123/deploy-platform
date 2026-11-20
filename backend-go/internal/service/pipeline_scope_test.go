package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAnalyzeYunxiaoPipelineScopeDetectsWritesWithoutReturningDefinition(t *testing.T) {
	pipeline := YunxiaoPipeline{
		Name:       "orders config deploy",
		Type:       "PIPELINEASCODE",
		UpdateTime: 1770000000000,
	}
	pipeline.PipelineConfig.Flow = `schema: tb
pipeline:
  - name: deploy
    stages:
      - name: production
        jobs:
          - displayName: apply-config
            task: execution-component-production@10
            params:
              steps:
                - name: config-update
                  stepType: exec-shell
                  command: kubectl create configmap orders --from-file=app.yaml # super-secret-value
                - name: image-update
                  stepType: exec-shell
                  command: kubectl set image deployment/orders api=registry.example.com/orders/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
                - name: chart-update
                  stepType: exec-shell
                  command: helm upgrade orders ./chart --namespace prod
                - name: arbitrary-script
                  stepType: exec-shell
                  command: echo $DEPLOY_TOKEN | ./deploy.sh
`

	report := AnalyzeYunxiaoPipelineScope("123456", pipeline)
	if report.AnalysisStatus != "manual_review_required" || !report.ReviewRequired {
		t.Fatalf("scope analysis must always require human review: %+v", report)
	}
	if len(report.DefinitionHash) != 64 || report.PipelineID != "123456" || report.UpdateTime != pipeline.UpdateTime {
		t.Fatalf("pipeline identity/hash missing: %+v", report)
	}
	wantCategories := map[string]bool{
		"opaque_task":             false,
		"possible_config_write":   false,
		"possible_workload_write": false,
		"helm_release_write":      false,
		"opaque_script":           false,
	}
	for _, finding := range report.Findings {
		if _, ok := wantCategories[finding.Category]; ok {
			wantCategories[finding.Category] = true
		}
		if finding.NodeRef == "" || strings.Contains(finding.NodeRef, "apply-config") || strings.Contains(finding.Summary, "kubectl") {
			t.Fatalf("finding must use a structural reference and fixed summary: %+v", finding)
		}
	}
	for category, found := range wantCategories {
		if !found {
			t.Errorf("missing %s finding: %+v", category, report.Findings)
		}
	}
	if len(report.UnresolvedNodes) == 0 {
		t.Fatal("shell and template nodes must remain unresolved")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"super-secret-value", "DEPLOY_TOKEN", "registry.example.com", "kubectl create", "helm upgrade"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("definition content leaked in report: %q", secret)
		}
	}
	if !strings.Contains(report.Warning, "无法证明流水线完整写入范围") {
		t.Fatalf("manual review warning missing: %q", report.Warning)
	}
}

func TestAnalyzeYunxiaoPipelineScopeNeverClaimsReadOnly(t *testing.T) {
	for name, pipeline := range map[string]YunxiaoPipeline{
		"missing definition": {Name: "ui-pipeline", Type: "UI"},
		"malformed definition": {
			Name: "invalid-yaml", Type: "PIPELINEASCODE",
			Content: "pipeline: [unterminated",
		},
		"oversized definition": {
			Name: "large", Type: "PIPELINEASCODE",
			Content: strings.Repeat("x", maxPipelineScopeDefinitionBytes+1),
		},
	} {
		t.Run(name, func(t *testing.T) {
			report := AnalyzeYunxiaoPipelineScope("456", pipeline)
			if !report.ReviewRequired {
				t.Fatal("scope check must not infer read-only")
			}
			if report.AnalysisStatus == "manual_review_required" {
				t.Fatalf("invalid/unavailable input reported as analyzed: %+v", report)
			}
			if len(report.Findings) != 0 || len(report.UnresolvedNodes) != 0 {
				t.Fatalf("invalid input produced guessed findings: %+v", report)
			}
		})
	}
}

func TestAnalyzeYunxiaoPipelineScopeInspectsNestedTaskDefinitionsAndCommandLists(t *testing.T) {
	pipeline := YunxiaoPipeline{
		Name: "structured task config",
	}
	pipeline.PipelineConfig.Flow = `stages:
  - jobs:
      - steps:
          - task:
              plugin: shell-runner@2
              params:
                script:
                  - kubectl
                  - apply
                  - -f deployment.yaml
          - command:
              shell:
                - helm upgrade orders ./chart
                - --namespace production
          - template:
              reference: shared-release-template
`

	report := AnalyzeYunxiaoPipelineScope("pipeline-structured", pipeline)
	if report.AnalysisStatus != "manual_review_required" || !report.ReviewRequired {
		t.Fatalf("structured command forms must still require review: %+v", report)
	}
	want := map[string]bool{
		"opaque_task":             false,
		"possible_workload_write": false,
		"helm_release_write":      false,
		"opaque_script":           false,
	}
	for _, finding := range report.Findings {
		if _, ok := want[finding.Category]; ok {
			want[finding.Category] = true
		}
	}
	for category, found := range want {
		if !found {
			t.Errorf("missing %s finding for structured pipeline input: %+v", category, report.Findings)
		}
	}
	if len(report.UnresolvedNodes) == 0 {
		t.Fatal("nested task and script definitions must remain unresolved")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, commandDetail := range []string{"deployment.yaml", "helm upgrade orders", "shared-release-template"} {
		if strings.Contains(string(encoded), commandDetail) {
			t.Fatalf("structured pipeline content leaked in report: %q", commandDetail)
		}
	}
}

func TestAnalyzeYunxiaoPipelineScopeBoundsDepthAndNodeCount(t *testing.T) {
	deep := strings.Builder{}
	deep.WriteString("pipeline:\n")
	for depth := 0; depth < maxPipelineScopeAnalysisDepth+8; depth++ {
		deep.WriteString(strings.Repeat(" ", (depth+1)*2))
		deep.WriteString("nested:\n")
	}

	wide := strings.Builder{}
	wide.WriteString("steps:\n")
	for i := 0; i < maxPipelineScopeAnalysisNodes+8; i++ {
		wide.WriteString("  - echo ok\n")
	}
	aliases := strings.Builder{}
	aliases.WriteString("pipeline:\n  layer0: &layer0 [echo ok]\n")
	for layer := 1; layer <= 20; layer++ {
		fmt.Fprintf(&aliases, "  layer%d: &layer%d [*layer%d, *layer%d]\n", layer, layer, layer-1, layer-1)
	}
	aliases.WriteString("  steps: *layer20\n")

	for name, definition := range map[string]string{
		"nested structure": deep.String(),
		"wide structure":   wide.String(),
		"alias expansion":  aliases.String(),
	} {
		t.Run(name, func(t *testing.T) {
			pipeline := YunxiaoPipeline{Name: "complex definition"}
			pipeline.PipelineConfig.Flow = definition
			report := AnalyzeYunxiaoPipelineScope("123456", pipeline)
			if report.AnalysisStatus != "manual_review_required" || !report.ReviewRequired {
				t.Fatalf("complex definitions must remain unapproved for analysis: %+v", report)
			}
			if !strings.Contains(report.Warning, "复杂度上限") || len(report.UnresolvedNodes) == 0 {
				t.Fatalf("analysis limit must expose unresolved structure: %+v", report)
			}
		})
	}
}
