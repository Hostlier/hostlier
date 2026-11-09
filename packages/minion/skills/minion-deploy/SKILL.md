---
name: minion-deploy
description: Build Minion container artifacts and deploy or update @fused/minion apps through Hostlier, including Fused secret bindings, readiness checks and scoped deployment profiles. Use for Minion packaging and deployment, not generic Hostlier server installation or provider SDK generation.
---

# Package and deploy Minion

Minion is a Hostlier product. Install Node 24+ and run `hostlier minion install` in the app directory using the complete Hostlier release. Keep the installed `.minion/framework/` archive with the npm lockfile.

A Minion build reads selected Fused contracts, generates native typed service bindings, checks the app, and creates a Docker context plus a native Hostlier project or pending consent artifact. It does not publish a Fused app, issue tokens, push an image or deploy. Keep those steps distinct in execution and reporting.

Read [deployment.md](references/deployment.md) for build configuration, commands and verification. Use an existing app and the user's selected registry, runtime architecture, controller and domain. Reuse an authorized Hostlier profile rather than provisioning a new VM or controller implicitly.

Local testing uses `hostlier minion dev` with `{ "entry": "app.ts" }` and optional `ui.entry` in minion.json. It needs no deployment profile or Docker. Selected integrations still require their Fused configuration and authorized execution environment. Deployment adds the explicit artifact settings below.

## Prepare the artifact

Use `fused.services` for new integrations: alias keys map to `{ service, version, operations }`. `hostlier minion prepare minion.json` generates editor types, and build runs the same preparation automatically. Keep the contract-reading `FUSED_API_KEY` in the process environment; no management key belongs in configuration or the runtime image. Application code imports `services` directly from `@fused/minion`.

- Use the installed Minion version's `BuildOptions`; its current generator accepts a root HTTPS hostname without a custom route port or path prefix. Do not apply every generic Hostlier endpoint feature to this generator without checking its implementation.
- Minion supplies the pinned Node base image for local builds. Registry deployments require an explicitly versioned app image. Match the target server's CPU architecture when building on a different laptop architecture.
- Choose either an existing immutable Engine app UUID and execution-token secret, or compiler consent mode with `engineUrl`, `grpcUrl`, explicit `version`, and a new `tokenSecret` name. Consent mode emits `fused-access.json` and `hostlier-pending.json`; run `hostlier fused provision NAME --file .../hostlier-pending.json`, complete the owner consent, then use the resulting `.hostlier`. `secrets` maps environment names to stored secret names, never secret values.
- Keep one stable Fused connection name per Minion across releases. A new release increments `fused.version` and uses a new `tokenSecret`; the controller-only `app.api.deploy` grant is scoped to that app family and its approved operation/bucket ceiling. Expanded scope requires renewed consent.
- The app's own authentication secret is separate from its Fused execution token. No runtime secrets belong in the Docker build context, arguments or image.
- Choose a fresh output directory; Minion deliberately refuses to overwrite an existing build. Inspect the generated `.hostlier` and artifact before publishing.

If Fused publication or credentials are missing, use `$minion-fused` when available. Continue independent build/test work while waiting for necessary identity references, but label any fixture identity as unsuitable for deployment.

## Deploy through the controller

Within the user's authorized deployment scope, build and publish the image, then run Hostlier validation, plan and deploy against the intended saved server profile. Use the generated `.hostlier` file explicitly so the working directory cannot select another app. Complete the reviewable artifact and plan before requesting any approval still required by the controller.

A valid scoped deployment credential can authorize the operation without an extra confirmation. If the controller requires owner approval, surface its real browser URL or request information; let the owner complete the passkey ceremony. Do not generate a fake hardware authenticator, disable verification or broaden a grant to bypass rejection. Hardware keys are optional: browser passkeys and appropriately scoped credentials are supported.

A limited deploy profile cannot upload secrets or administer accounts. Ask the administrator to provision the named secret through an authorized administrative path when needed. Retain provider credentials inside Fused. Use hidden prompts or the existing secret-manager/stdin flow; do not ask the user to paste secret values into chat.

## Confirm the deployed behavior

Wait for the deployment operation to finish, then verify controller status, app HTTPS health and a real action. Check the rendered JSX UI when present. Distinguish successful image publication, successful deployment, Engine admission and an actual provider response. Use app/provider timestamps to identify cached or demo data.

If a call or deployment fails, inspect its operation status before retrying. Do not automatically replay provider mutations. A generated server waits for initial Engine admission before listening; later Engine failures can leave `/healthz` healthy while actions fail.

Preserve previous image/build references for the user's rollback workflow. A rollback also needs its captured Engine version and token to remain usable. Deleting an app does not revoke a possibly shared Engine token. Report the actual deployed URL, image/version, relevant checks and any remaining limitations; do not call a local preview a deployment.
