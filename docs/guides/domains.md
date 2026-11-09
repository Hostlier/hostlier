# Domains and routes

[All guides](../../GUIDE.md)

The server's `/etc/hostlier/config.yaml` declares supported domains. Route selection belongs to each deployment:

```yaml
version: 1
domains:
  - example.com
  - apps.example.net
```

Configure the corresponding DNS records yourself. A wildcard record such as `*.example.com` lets deployments use `analytics.example.com`; the apex needs its own DNS record if you also use `example.com/analytics`.

`hostlier domains add DOMAIN` updates the server configuration. After editing the file directly, restart `hostlier.service` to apply it. Removing domains that still serve applications or accepted deployments is rejected.

Next: [Deploy an application](deploy.md). For an existing proxy, see [Nginx coexistence](../nginx-coexistence.md).
