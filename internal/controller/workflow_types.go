package controller

import "hostlier/pkg/workflow"

// WorkflowPlan exposes only reviewed metadata and secret references, never opaque package payloads.
type WorkflowPlan struct {
	Base       string               `json:"base,omitempty"`
	Previous   string               `json:"previous,omitempty"`
	Inputs     string               `json:"inputs,omitempty"`
	ID         string               `json:"id"`
	Key        string               `json:"key"`
	Step       workflow.Step        `json:"step"`
	Digest     string               `json:"digest"`
	Summary    []string             `json:"summary"`
	SecretRefs map[string]SecretRef `json:"secretRefs,omitempty"`
	Reuse      bool                 `json:"reuse"`
}

// WorkflowRecord retains ownership and encrypted results independently of application lifecycle.
type WorkflowRecord struct {
	Package    string               `json:"package,omitempty"`
	Prior      *WorkflowRecord      `json:"prior,omitempty"`
	PlanID     string               `json:"planId,omitempty"`
	SpecHash   string               `json:"specHash"`
	Status     string               `json:"status"`
	Result     string               `json:"result,omitempty"`
	SecretRefs map[string]SecretRef `json:"secretRefs,omitempty"`
}
