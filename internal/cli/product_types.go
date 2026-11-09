package cli

import (
	"hostlier/internal/model"
	"hostlier/pkg/product"
)

// productProject is a local composition document whose expansion follows ordinary deployment approval.
type productProject struct {
	Version    int                `yaml:"version"`
	Product    product.Definition `yaml:"product"`
	Inputs     map[string]any     `yaml:"inputs,omitempty"`
	SecretRefs map[string]string  `yaml:"secretRefs,omitempty"`
	App        model.Project      `yaml:"app"`
}
