---
name: hostlier-approve
description: Review and sign a pending Hostlier request from a human-owned terminal with a FIDO security key.
---

# Hostlier approve

`hostlier approve CODE --key FILE [--url https://CONTROLLER]` displays the retained request and asks the human to confirm, enter their key PIN and touch it. The URL selects the controller without requiring the agent API credential. An agent must not answer for the human or impersonate their authenticator. Codes identify requests; they are not approval credentials. Have the human run this on their trusted machine.
