package controller

// retainSecretRevision publishes one encrypted revision without discarding rollback history.
func (c *Controller) retainSecretRevision(name, revision, ciphertext string) {
	secret := c.state.Secrets[name]
	// A previously unseen name starts with an empty revision history.
	if secret == nil {
		secret = &Secret{Revisions: map[string]string{}}
		c.state.Secrets[name] = secret
	}
	secret.Current = revision
	secret.Revisions[revision] = ciphertext
}

// unchangedSecretRevision avoids creating revisions or changing generation for identical values.
func (c *Controller) unchangedSecretRevision(name, value string) (string, error) {
	secret := c.state.Secrets[name]
	// The first upload always creates a new encrypted revision.
	if secret == nil {
		return "", nil
	}
	current, err := c.store.Decrypt(secret.Revisions[secret.Current])
	// Authentication failures in retained ciphertext must remain errors.
	if err != nil {
		return "", err
	}
	// Only exact byte equality can reuse a revision referenced by running workloads.
	if current == value {
		return secret.Current, nil
	}
	return "", nil
}
