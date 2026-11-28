package config

import (
	"testing"

	"hostlier/internal/model"
)

func TestCronValidation(t *testing.T) {
	job := model.Project{
		Version: 2, Kind: model.CronKind, Name: "report", Schedule: " 0 7 * * 1-5 ",
		Source:    &model.Source{Image: "example/report:1"},
		Resources: &model.Limits{CPU: "1", Memory: "256Mi"},
		Requests:  &model.Limits{CPU: "0.25", Memory: "128Mi"},
	}
	normalized, err := ValidateProject(job)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Schedule != "0 7 * * 1-5" || normalized.TimeZone != "Etc/UTC" || normalized.Image != job.Source.Image {
		t.Fatalf("unexpected normalized schedule: %+v", normalized)
	}
	for _, tc := range []struct {
		name   string
		change func(*model.Project)
	}{
		{"job requires v2", func(p *model.Project) { p.Version = 1 }},
		{"schedule required", func(p *model.Project) { p.Schedule = "" }},
		{"invalid calendar", func(p *model.Project) { p.Schedule = "61 * * * *" }},
		{"interval descriptor", func(p *model.Project) { p.Schedule = "@every 5m" }},
		{"embedded timezone", func(p *model.Project) { p.Schedule = "TZ=Etc/UTC 0 0 * * *" }},
		{"invalid time zone", func(p *model.Project) { p.TimeZone = "Europe/Nowhere" }},
		{"service listener", func(p *model.Project) { p.Port = 8080 }},
		{"service schedule", func(p *model.Project) { p.Kind, p.Port = "", 8080 }},
		{"unbounded requests", func(p *model.Project) { p.Resources = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := job
			tc.change(&input)
			if _, err := ValidateProject(input); err == nil {
				t.Fatal("invalid schedule was accepted")
			}
		})
	}
}

func TestServiceCronValidation(t *testing.T) {
	var spec model.Project
	yaml := []byte("version: 1\nname: report\nimage: example/report:1\nport: 8080\ncron:\n  daily:\n    schedule: '@daily'\n    command: [/app/report]\n")
	if err := DecodeYAML(yaml, &spec); err != nil {
		t.Fatal(err)
	}
	normalized, err := ValidateProject(spec)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Cron["daily"].TimeZone != "Etc/UTC" || spec.Cron["daily"].TimeZone != "" {
		t.Fatalf("normalization must preserve the authored job: %+v, %+v", normalized.Cron, spec.Cron)
	}
	v2 := spec
	v2.Version = 2
	v2.Source = &model.Source{Image: v2.Image}
	v2.Endpoint = &model.Endpoint{Exposure: "public", Port: v2.Port}
	if _, err := ValidateProject(v2); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]model.CronSchedule{
		{"bad/name": {Schedule: "@daily", Command: []string{"/app/report"}}},
		{"bad-": {Schedule: "@daily", Command: []string{"/app/report"}}},
		{"daily": {Schedule: "@daily"}},
		{"daily": {Schedule: "99 * * * *", Command: []string{"/app/report"}}},
	} {
		spec.Cron = bad
		if _, err := ValidateProject(spec); err == nil {
			t.Fatalf("accepted invalid service jobs: %+v", bad)
		}
	}
}
