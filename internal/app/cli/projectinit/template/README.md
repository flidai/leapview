# LeapView analytics starter

This project is portable analytics source plus a small deterministic synthetic
fixture. It contains no Project, access, publication, target, or credential
authority.

Start the checkout-scoped local runtime:

```sh
leapview dev
```

The declared development input is `.leapview/development-inputs.yaml`.
`leapview dev` verifies its bounded synthetic provenance and exact file digests,
then stages the immutable managed-data revision to the checkout-scoped local
runtime before synchronizing the first candidate. Restarting `dev` safely
reuses the same revision and retained local volumes.

Editing YAML does not restage or refresh the fixture. Changing `sales.csv`
requires updating its declaration and restarting `dev`, which stages a new
immutable revision after validating the complete declaration. Never replace
this sample with a production download or production credential.
