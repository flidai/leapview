# Raw CodeQL health fixtures

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

`go-raw-health.sarif` projects the same fields from run 37264715434, job
111618965827, PR merge candidate 4eaf9423dccfa6ab0b3271f56f62d6a524f33fb5
(branch head 5232a110276ae903b3762b7be59798bd45fe9bae). It retains five
successful extraction notifications covering all four Go modules and generated
SQL source, together with the analyzer query-pack identities.
CodeQL CLI 2.27.1 and the same pinned action. Original raw file SHA-256:
`791ec4a34053c1bda9d18d682e940d3f24aed8dc02fa63ec6adeba154c17c29c`.

Source artifact:
https://github.com/flidai/leapview/actions/runs/37264715434/artifacts/11325868794

Language verification requires the pinned analyzer's actual `codeql/go-queries`
or `codeql/javascript-queries` extension, in addition to the workflow-supplied
upload category. A relabeled report is not evidence of the other language.
