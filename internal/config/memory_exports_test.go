package config

import (
	"hostlier/internal/model"
	"os"
	"path/filepath"
	"testing"
)

func TestMemorySchema(t *testing.T) {
	base := model.Project{Version: 3, Kind: "memory", Name: "library", Exports: map[string]model.MemoryExport{"files": {Path: "/srv/existing", Protocols: []string{"webdav", "s3"}}}}
	if _, err := ValidateProject(base); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInput(base); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*model.Project){
		"version": func(p *model.Project) { p.Version = 2 },
		"kind":    func(p *model.Project) { p.Kind = "" },
		"image":   func(p *model.Project) { p.Image = "nginx" },
		"route":   func(p *model.Project) { p.Route = "example.com" },
		"source":  func(p *model.Project) { p.Source = &model.Source{Image: "nginx"} },
		"root": func(p *model.Project) {
			p.Exports = map[string]model.MemoryExport{"files": {Path: "/", Protocols: []string{"s3"}}}
		},
		"traversal": func(p *model.Project) {
			p.Exports = map[string]model.MemoryExport{"files": {Path: "/srv/a/../b", Protocols: []string{"s3"}}}
		},
		"smb": func(p *model.Project) {
			p.Exports = map[string]model.MemoryExport{"files": {Path: "/srv/files", Protocols: []string{"smb"}}}
		},
		"duplicate": func(p *model.Project) {
			p.Exports = map[string]model.MemoryExport{"files": {Path: "/srv/files", Protocols: []string{"s3", "s3"}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := base
			mutate(&p)
			if _, err := ValidateProject(p); err == nil {
				t.Fatal("accepted invalid memory config")
			}
		})
	}
	dir := t.TempDir()
	file := filepath.Join(dir, ".hostlier")
	yaml := "version: 3\nkind: memory\nname: library\nexports:\n  files:\n    path: /existing/on/controller\n    protocols: [webdav, s3]\n    readOnly: true\n"
	if err := os.WriteFile(file, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProject(file); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(file, []byte(yaml+"    unexpected: true\n"), 0600)
	if _, err := ReadProject(file); err == nil {
		t.Fatal("unknown field accepted")
	}
}
