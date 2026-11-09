package model

// FusedConnection binds a workload to one immutable Engine app and an existing execution-token secret.
// Provider credentials and Engine management credentials are never part of this declaration.
type FusedConnection struct {
	GRPCURL     string `json:"grpcUrl" yaml:"grpcUrl"`
	AppID       string `json:"appId" yaml:"appId"`
	TokenSecret string `json:"tokenSecret" yaml:"tokenSecret"`
}

// FusedBucket exposes approved routing identities without provider credentials.
type FusedBucket struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
