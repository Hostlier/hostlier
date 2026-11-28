package cli

import (
	"net/url"
	"strings"

	"hostlier/internal/model"
)

const cronHelp = `Manage scheduled K3s jobs

Register a cron project with hostlier plan --file JOB.hostlier and hostlier deploy --file JOB.hostlier.

Usage:
  hostlier cron status NAME
  hostlier cron pause NAME
  hostlier cron resume NAME
  hostlier cron run NAME
  hostlier cron run-now NAME
  hostlier cron history NAME
  hostlier cron logs NAME RUN_ID [--tail N]
  hostlier cron delete NAME

Use --server PROFILE and --on HOST to select the controller or fleet worker.
Use SERVICE/JOB as NAME for a job declared under cron in a service file.
`

func (s *session) cronCommand() error {
	if s.target == "" {
		return model.Err("ARGUMENTS", "Specify a cron schedule name. See hostlier cron --help")
	}
	args := s.parsed.args
	app, job, attached := strings.Cut(s.target, "/")
	if app == "" || attached && (job == "" || strings.Contains(job, "/")) {
		return model.Err("ARGUMENTS", "Use a standalone cron name or SERVICE/JOB")
	}
	path := "/v1/apps/" + url.PathEscape(app) + "/cron"
	if attached {
		path += "/jobs/" + url.PathEscape(job)
	}
	if s.sub == "logs" && len(args) == 4 {
		return s.followLogs(path + "/runs/" + url.PathEscape(args[3]) + "/logs")
	}
	if len(args) != 3 {
		return model.Err("ARGUMENTS", "See hostlier cron --help for the command syntax")
	}
	switch s.sub {
	case "status":
		return s.getPrint(path)
	case "history":
		return s.getPrint(path + "/runs")
	case "pause", "resume", "run", "run-now", "delete":
		action := s.sub
		if action == "run-now" {
			action = "run"
		}
		var schedule struct {
			Revision string `json:"revision"`
		}
		if err := s.request("GET", path, nil, &schedule); err != nil {
			return err
		}
		var result operationReceipt
		if err := s.request("POST", path+"/action", map[string]string{"action": action, "revision": schedule.Revision}, &result); err != nil {
			return err
		}
		return s.observeAccepted(result)
	default:
		return model.Err("ARGUMENTS", "Unknown cron command. See hostlier cron --help")
	}
}
