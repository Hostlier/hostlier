package model

import "context"

const CronKind = "cron"

// CronSchedule defines a run-to-completion command using its service's image and inputs.
type CronSchedule struct {
	Schedule string   `json:"schedule" yaml:"schedule"`
	TimeZone string   `json:"timeZone,omitempty" yaml:"timeZone,omitempty"`
	Command  []string `json:"command" yaml:"command"`
	Args     []string `json:"args,omitempty" yaml:"args,omitempty"`
}

// ServiceCronProject projects the primary container's approved settings into a batch workload.
func ServiceCronProject(service Project, schedule CronSchedule) Project {
	return Project{
		Version: 2, Kind: CronKind, Name: service.Name, Image: service.Image, Source: &Source{Image: service.Image},
		Schedule: schedule.Schedule, TimeZone: schedule.TimeZone, Command: schedule.Command, Args: schedule.Args,
		Env: service.Env, Secrets: service.Secrets, Configs: service.Configs, Security: service.Security,
		Resources: service.Resources, Requests: service.Requests, Shutdown: service.Shutdown,
	}
}

// CronRun exposes job state without exposing Kubernetes manifests or credentials.
type CronRun struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Trigger     string `json:"trigger"`
	CreatedAt   string `json:"createdAt"`
	ScheduledAt string `json:"scheduledAt,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
}

// CronRuntime manages scheduled batch workloads on a capable runtime.
type CronRuntime interface {
	DeployCron(context.Context, Project, DeployContext) (Deployment, error)
	CronAction(context.Context, string, string, string, string) (string, error)
	CronRuns(context.Context, string) ([]CronRun, error)
	CronLogs(context.Context, string, string, int) (string, error)
}

// ServiceCronRuntime reconciles schedules independently of service pod lifecycles.
type ServiceCronRuntime interface {
	CronRuntime
	ReconcileServiceCrons(context.Context, Project, DeployContext) error
	ServiceCronStatus(context.Context, string) (string, error)
}
