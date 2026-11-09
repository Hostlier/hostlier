# Products and workflows

[All guides](../../GUIDE.md)

Compose installed packages with typed inputs, secret references and output bindings using [local products](../local-products.md):

```sh
hostlier product plan --file product.yaml
hostlier product deploy --file product.yaml
hostlier workflow list analytics
hostlier workflow inspect analytics database
hostlier workflow reconcile analytics database
```

Reconciliation independently inspects the original package's resources, then requires approval to recover verified results or clear a verified absent attempt. An uncertain result remains blocked; provisioning is never blindly replayed.

Controller state uses embedded SQLite with automatic migration from the original JSON state. [Controller backup and recovery](../controller-recovery.md) covers the protected metadata bundle, configuration, key and installed packages. Application volumes and database contents need separate backups.

See also [database resources](databases.md) and [scheduled database backups](../database-recovery.md).
