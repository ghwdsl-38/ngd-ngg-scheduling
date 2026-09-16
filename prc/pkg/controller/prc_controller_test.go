package controller

import "testing"

func TestAlgorithmFailureDetailsUsesRootCause(t *testing.T) {
	reason, message := algorithmFailureDetails(AlgorithmResponse{Failure: &AlgorithmFailure{
		Code:    "QUOTA_PREVENTS_MINIMUM",
		Message: "3 nodes matched, but no node group can satisfy minResources within quota cpu=80000m,memory=409600Mi",
	}})
	if reason != "QUOTA_PREVENTS_MINIMUM" {
		t.Fatalf("reason=%q", reason)
	}
	if message != "3 nodes matched, but no node group can satisfy minResources within quota cpu=80000m,memory=409600Mi" {
		t.Fatalf("message=%q", message)
	}
}

func TestAlgorithmFailureDetailsHasFallback(t *testing.T) {
	reason, message := algorithmFailureDetails(AlgorithmResponse{})
	if reason != "NO_FEASIBLE_NODE_GROUP" || message != "Algorithm returned no feasible node group" {
		t.Fatalf("reason=%q message=%q", reason, message)
	}
}
