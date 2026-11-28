package config

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"hostlier/internal/model"
)

func validateCronProject(p model.Project) (model.Project, error) {
	if p.Version != 2 {
		return p, invalid("kind: cron requires version 2")
	}
	if p.Workload != "" || p.Port != 0 || p.Endpoint != nil || len(p.Endpoints) != 0 || p.PrimaryEndpoint != "" || p.Route != "" || p.Proxy != nil || p.Health != nil || p.Replicas != nil || p.Autoscaling != nil || p.SleepAfter != "" {
		return p, invalid("Cron jobs do not accept service, routing, health or replica settings")
	}
	if len(p.Cron) != 0 || len(p.Sidecars) != 0 || len(p.Volumes) != 0 || len(p.Dependencies) != 0 || len(p.Bindings) != 0 || p.Workflow != nil || p.Fused != nil || len(p.Exports) != 0 || p.NamespaceImage != "" || p.Required != nil {
		return p, invalid("Standalone cron jobs do not accept sidecars, persistent volumes, dependencies, workflows or exports")
	}
	schedule, err := normalizeCronSchedule(model.CronSchedule{Schedule: p.Schedule, TimeZone: p.TimeZone})
	if err != nil {
		return p, err
	}
	p.Schedule, p.TimeZone = schedule.Schedule, schedule.TimeZone
	if err := validateProcessCommand(p); err != nil {
		return p, err
	}
	if p.Requests != nil {
		if err := validateRequests(p); err != nil {
			return p, err
		}
	}
	for _, validate := range []func(model.Project) (model.Project, error){
		normalizeSource, validateProjectIdentity, validateProjectResources, validateProjectLimits,
		validateProjectEnvironment, validateProjectSecrets, validateManagedConfigs, validateContainerSecurity,
	} {
		if p, err = validate(p); err != nil {
			return p, err
		}
	}
	return p, nil
}

func normalizeCronSchedule(schedule model.CronSchedule) (model.CronSchedule, error) {
	schedule.Schedule = strings.TrimSpace(schedule.Schedule)
	if len(schedule.Schedule) == 0 || len(schedule.Schedule) > 128 || strings.Contains(schedule.Schedule, "TZ=") || strings.HasPrefix(schedule.Schedule, "@every") {
		return schedule, invalid("Specify a Kubernetes cron schedule and a separate timeZone")
	}
	if _, err := cron.ParseStandard(schedule.Schedule); err != nil {
		return schedule, invalid("Invalid cron schedule")
	}
	if schedule.TimeZone == "" {
		schedule.TimeZone = "Etc/UTC"
	}
	if _, err := time.LoadLocation(schedule.TimeZone); err != nil || schedule.TimeZone == "Local" || len(schedule.TimeZone) > 128 {
		return schedule, invalid("Use a named time zone such as Etc/UTC or Europe/London")
	}
	return schedule, nil
}

func validateServiceCron(p model.Project) (model.Project, error) {
	if len(p.Cron) == 0 {
		return p, nil
	}
	if p.Workload == "worker" {
		return p, invalid("cron requires an application service")
	}
	schedules := make(map[string]model.CronSchedule, len(p.Cron))
	for _, name := range slices.Sorted(maps.Keys(p.Cron)) {
		schedule := p.Cron[name]
		if !nameRE.MatchString(name) || strings.HasSuffix(name, "-") {
			return p, invalid("A cron job name must start with a lowercase letter and end with a letter or digit")
		}
		if len(schedule.Command) == 0 || strings.TrimSpace(schedule.Command[0]) == "" {
			return p, invalid("Each service cron job requires an explicit command")
		}
		schedule, err := normalizeCronSchedule(schedule)
		if err != nil {
			return p, err
		}
		if err := validateCommands(model.ServiceCronProject(p, schedule)); err != nil {
			return p, err
		}
		schedules[name] = schedule
	}
	p.Cron = schedules
	return p, nil
}
