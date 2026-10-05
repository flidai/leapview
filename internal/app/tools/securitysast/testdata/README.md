# Raw CodeQL health fixture

`javascript-raw-health.sarif` projects the health fields from the **raw analyzer
artifact**, not GitHub's processed code-scanning SARIF, from run 37263931523,
job 111616654768, candidate branch head 827e16fc26fbcca512156a3dd9faf3247cfddfab.
CodeQL CLI 2.27.1; pinned action 2892aa5e19bbd11bc0cff5427e3b750a04d9e3c2.
Original raw file SHA-256:
`cab60f74e9fceae33af1341762d578671f413e340dabb38663557edac54f2eff`.

The fixture retains the original category, tool identities and notification
descriptors, successful invocation, and two real extraction notifications
(desktop accessibility code and generated signal contracts). Unrelated rules,
results, artifacts, and successful-file notifications are omitted. Artifact
indexes are removed from retained locations because the artifact table is
omitted. Empty notification messages with `level: none` are valid actual output.

Source artifact:
https://github.com/flidai/leapview/actions/runs/37263931523/artifacts/11324709830
