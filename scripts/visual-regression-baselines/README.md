# UI visual-regression baselines

`task qa:ui-framework` compares the representative dashboard states in this
directory after the existing route QA passes. The matrix covers Executive Sales
overview, Visual Showcase overview, and the Visual Showcase tables page in light
and dark modes at desktop and compact viewports.

To intentionally refresh the baselines after reviewing a UI change, run:

```sh
task qa:ui-framework:visual:update
```

Review every changed PNG before committing it. Baselines are never accepted
automatically in CI. The update task immediately starts a fresh Playwright
comparison after writing the images, so a one-off cold rendering cannot be
accepted as a baseline. A comparison failure links the expected baseline and
writes the actual image, diff, failure screenshot, trace, video, and HTML report
under `.tmp/qa-ui-framework/visual-artifacts`. Merge-queue and nightly CI retain
that directory together with the expected baselines as a downloadable failure
artifact.
