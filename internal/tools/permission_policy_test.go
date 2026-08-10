package tools

import (
	"context"
	"strings"
	"testing"
)

func TestApplyRiskPolicy(t *testing.T) {
	allow := func(context.Context, PermissionRequest) (bool, error) { return true, nil }
	deny := func(context.Context, PermissionRequest) (bool, error) { return false, nil }
	high := PermissionRequest{Tool: "git_restore", Risk: RiskHigh}
	low := PermissionRequest{Tool: "run_command", Risk: RiskLow}

	policy, err := ApplyRiskPolicy(allow, deny, RiskPolicyDeny)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, _ := policy(context.Background(), high); allowed {
		t.Fatal("high-risk deny policy allowed request")
	}
	if allowed, _ := policy(context.Background(), low); !allowed {
		t.Fatal("low-risk request should retain base allow")
	}

	policy, err = ApplyRiskPolicy(allow, deny, RiskPolicyConfirm)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, _ := policy(context.Background(), high); allowed {
		t.Fatal("high-risk confirmation should use denying confirmer")
	}
	policy, _ = ApplyRiskPolicy(nil, nil, RiskPolicyConfirm)
	if _, err := policy(context.Background(), high); err == nil || !strings.Contains(err.Error(), "confirmation handler is unavailable") {
		t.Fatalf("missing confirmer error = %v", err)
	}
}
