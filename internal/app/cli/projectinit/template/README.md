# LeapView analytics starter

This project is portable analytics source plus a small deterministic synthetic
fixture. It contains no Project, access, publication, target, or credential
authority.

Start the checkout-scoped local runtime:

```sh
leapview dev
```

The declared development input is `.leapview/development-inputs.yaml`. Inspect
the exact managed-data revision before staging it:

```sh
leapview data plan --development-input sample
```

Stage only after reviewing that result, using the target and Project identity
created by the local runtime:

```sh
leapview data sync --development-input sample --target http://127.0.0.1:<port> --project-id <project-id> --environment dev
```

Editing YAML does not restage or refresh the fixture. Changing `sales.csv`
creates a different immutable managed-data revision on the next explicit sync.
Never replace this sample with a production download or production credential.
