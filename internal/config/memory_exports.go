package config

import (
	"hostlier/internal/model"
	"path"
	"reflect"
	"strings"
)

// validateMemoryProject deliberately has no workload defaults or host I/O.
func validateMemoryProject(p model.Project) (model.Project, error) {
	if p.Version != 3 || p.Kind != "memory" || !nameRE.MatchString(p.Name) || len(p.Exports) == 0 || len(p.Exports) > 32 {
		return p, invalid("Memory projects require version: 3, kind: memory, a name and 1–32 exports")
	}
	rest := p
	rest.Version, rest.Kind, rest.Name, rest.Exports = 0, "", "", nil
	if !reflect.DeepEqual(rest, model.Project{}) {
		return p, invalid("Memory projects accept only version, kind, name and exports; workload fields are forbidden")
	}
	for name, export := range p.Exports {
		if !nameRE.MatchString(name) || len(export.Path) > 4096 || !strings.HasPrefix(export.Path, "/") || export.Path == "/" || path.Clean(export.Path) != export.Path || strings.ContainsAny(export.Path, "\\\x00\r\n") {
			return p, invalid("Exports require a name and canonical absolute existing host path other than /")
		}
		if len(export.Protocols) == 0 || len(export.Protocols) > 2 {
			return p, invalid("Select webdav and/or s3 for each export")
		}
		seen := map[string]bool{}
		for _, protocol := range export.Protocols {
			if (protocol != "webdav" && protocol != "s3") || seen[protocol] {
				return p, invalid("Export protocols must be unique: webdav, s3")
			}
			seen[protocol] = true
		}
	}
	return p, nil
}
