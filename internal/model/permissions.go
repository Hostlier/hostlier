package model

// Permission identifies one independently delegable controller capability.
// Swarm permissions govern grouping independently of workload mutation rights.
const (
	ServersAdd     = "servers.add"
	ServersRemove  = "servers.remove"
	SwarmsManage   = "swarms.manage"
	SwarmsView     = "swarms.view"
	AppsDeploy     = "apps.deploy"
	AppsRollback   = "apps.rollback"
	AppsScale      = "apps.scale"
	AppsStart      = "apps.start"
	AppsStop       = "apps.stop"
	AppsRestart    = "apps.restart"
	AppsRemove     = "apps.remove"
	AppsPurge      = "apps.purge"
	SidecarsRemove = "sidecars.remove"
)

// ValidPermission rejects unknown names and wildcard authority.
func ValidPermission(name string) bool {
	switch name {
	case ServersAdd, ServersRemove, SwarmsManage, SwarmsView, AppsDeploy, AppsRollback, AppsScale,
		AppsStart, AppsStop, AppsRestart, AppsRemove, AppsPurge, SidecarsRemove:
		return true
	}
	return false
}
