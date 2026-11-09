---
name: hostlier-server
description: Add a fleet controller, or list and select saved Hostlier server profiles.
---

# Hostlier server

Use `hostlier server list` to inspect saved profiles and the selected box. Use `hostlier server list MASTER` to see a master’s enrolled boxes. `hostlier server use MASTER/BOX` selects the box for future workload commands. A box on the current master can be selected by name. `hostlier server use MASTER` selects the master itself.

Use `hostlier connect https://CONTROLLER` to create a laptop profile and `hostlier connect admin@BOX` to add and select a box over one SSH connection. `hostlier server add admin@BOX` enrolls it while keeping the current selection. The master must reach the box on TCP 7464. A local release directory can be supplied with `--release-dir DIR`.

From an installed box, run `sudo hostlier-controller connect MASTER_URL_OR_PROFILE` to join a master or switch to another one. The same workflow is available as `hostlier server join MASTER`. It uses the local administration socket, the master's enrollment review, and a mutually authenticated reachability check.
