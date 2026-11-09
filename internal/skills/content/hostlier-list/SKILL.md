---
name: hostlier-list
description: List applications deployed on a selected Hostlier server.
---

# Hostlier list

Run `hostlier list --server production --json` to retrieve the controller's application inventory. Use `hostlier server list` to inspect local profiles if the intended server is unclear; do not silently switch the user's selected profile.

Select applications by the identities returned by the controller. Use `hostlier status APP` for detailed state, `hostlier logs APP` for logs, or `hostlier usage APP` for observed allocation and usage. This command lists applications, not private database resources (`hostlier resource list`) or local profiles. Report the selected server along with findings when multiple servers are configured.
