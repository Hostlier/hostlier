# Hostlier

Hostlier deploys containers to servers you control. A controller on the server
runs apps, routes traffic, and manages HTTPS; a CLI on your laptop validates,
plans, and deploys each change.

## Quick start

1. Prepare an [Ubuntu server](docs/guides/install.md), a domain pointed at it,
   and ports 80 and 443. Download the matching controller bundle from
   [Releases](https://github.com/Hostlier/hostlier/releases). Verify the published
   checksum, then extract the controller bundle on the server. Build the CLI from
   source using the [build guide](docs/guides/build.md).
2. Install the controller. It shows the proposed changes before applying them:

   ```sh
   sudo ./hostlier-controller init --domain apps.example.com
   ```

3. Connect the CLI from your laptop:

   ```sh
   hostlier connect https://hostlier.apps.example.com --name production
   ```

4. Save this file as `.hostlier` in your app directory:

   ```yaml
   version: 2
   name: hello
   source:
     image: nginx:1.28-alpine
   endpoint:
     exposure: public
     port: 80
     route: https://hello.apps.example.com
   ```

5. Validate, review, and deploy:

   ```sh
   hostlier app validate
   hostlier app plan
   hostlier app deploy
   hostlier app status hello
   ```

Hostlier resolves the image during planning, asks for deployment approval, and
serves the app over HTTPS. Publish a new image and run `hostlier app deploy` again
to update it.

## Guides

- [Install and connect](docs/guides/install.md)
- [Deploy and update apps](docs/guides/deploy.md)
- [Private images](docs/guides/private-images.md)
- [Databases and storage](docs/guides/databases.md)
- [Minion apps](docs/guides/minion.md)
- [All guides](GUIDE.md)

To build Hostlier from source or contribute, see the [build guide](docs/guides/build.md)
and [validation notes](VALIDATION.md).
