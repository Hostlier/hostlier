package model

// Autoscaling delegates bounded CPU replica changes to the native Kubernetes HPA.
// Approval persists until an explicit pause or a replacement deployment.
type Autoscaling struct {
	MinReplicas      int `json:"minReplicas" yaml:"minReplicas"`
	MaxReplicas      int `json:"maxReplicas" yaml:"maxReplicas"`
	CPUTargetPercent int `json:"cpuTargetPercent" yaml:"cpuTargetPercent"`
}
