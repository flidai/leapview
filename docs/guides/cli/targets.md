# Targets and environments

The target tells the CLI which LeapView instance to contact. The environment is the instance identity that the CLI expects to find there. Use both for production-like work so a valid credential or copied command cannot silently reach the wrong instance.

## Choose explicit boundaries

Give development, staging, and production separate URLs and credentials. A project ID may exist in more than one instance, so it is not a sufficient deployment boundary by itself.

Create private candidates directly on the intended target:

```sh
leapview dev --project dashboards/leapview.yaml \
  --target https://dash.staging.example.com
```

LeapView reads the target's immutable instance identity and environment before synchronizing the project. The local candidate handoff is keyed by both the project path and the target origin, so `publish` cannot silently promote a candidate created on another target.

## Supply a target

For an occasional command, pass `--target` explicitly. For one CI job, set `LEAPVIEW_TARGET`. For repeated human use, [`leapview login`](/docs/cli/login) creates a device-authorized profile:

```sh
leapview login https://dash.staging.example.com
```

The profile pins the server-reported canonical origin and immutable instance ID. Only non-secret metadata is stored in the profile file; credentials remain in the OS-native store. Avoid DNS aliases that can be repointed between environments. Use `--name staging` during login if you want a stable local profile name, then use `--target staging` on later commands.

## Keep local validation separate

[`leapview validate`](/docs/cli/validate) compiles the project locally and does not require a target. Run it before contacting any environment:

```sh
leapview validate --project dashboards/leapview.yaml
```

Then use an explicit remote target for `dev` and `publish`. Keep the project path and target unchanged between review and publication. Target-owned connection evidence and managed-data pins are captured in the immutable candidate rather than supplied again at publication time.

## Verify before deployment

Check the target URL, workload project, and asserted environment in reviewable CI configuration, not only in an operator's shell history. Use separate service principals and protected secret scopes so a staging job cannot exchange a production workload credential.

Continue with [Develop, review, and publish](/docs/cli/validate-deploy) for the full promotion workflow and [`leapview dev`](/docs/cli/dev) for every authoring option.
