package client

import "hostlier/internal/model"

// profileCredentialKey supports legacy name-based records and atomically published renewals.
func profileCredentialKey(name string, profile Profile) string {
	// Legacy profiles retain their existing credential location until renewed.
	if profile.CredentialKey == "" {
		return name
	}
	return profile.CredentialKey
}

// Renew stages a fresh credential before atomically switching the profile's reference.
func (s Store) Renew(name string, expected Profile, id, token string) error {
	return s.RenewEnrollment(name, expected, id, token, expected.Enrollment)
}

// RenewEnrollment publishes authentication preferences with the matching replacement identity.
func (s Store) RenewEnrollment(name string, expected Profile, id, token string, enrollment Enrollment) error {
	p, err := s.List()
	// Do not replace a profile removed or changed while browser approval was pending.
	if err != nil {
		return err
	}
	// Fresh enrollment must be complete and still target the original saved profile.
	if actual, ok := p.Servers[name]; !ok || actual != expected {
		return model.Err("PROFILE_CHANGED", "Server profile changed during reauthentication; retry")
	}
	// Empty credentials must never replace a usable saved identity.
	if id == "" || token == "" {
		return model.Err("ENROLLMENT", "Controller returned no device credential")
	}
	secrets := map[string]string{}
	// Preserve all other profile credentials while staging the replacement.
	if err = s.read("credentials.json", &secrets); err != nil {
		return err
	}
	key := "device:" + id
	// A renewed identity must not collide with a retained credential.
	if _, exists := secrets[key]; exists {
		return model.Err("ENROLLMENT", "Controller returned a previously stored identity")
	}
	secrets[key] = token
	// Failure here leaves the original profile and credential selected.
	if err = s.write("credentials.json", secrets); err != nil {
		return err
	}
	p.Servers[name] = Profile{URL: expected.URL, ClientID: id, CredentialKey: key, Enrollment: enrollment}
	// Publishing one metadata file commits both identity and credential selection together.
	if err = s.write("profiles.json", p); err != nil {
		return err
	}
	delete(secrets, profileCredentialKey(name, expected))
	// Cleanup is best effort after commit; an interrupted cleanup cannot invalidate the new profile.
	_ = s.write("credentials.json", secrets)
	return nil
}
