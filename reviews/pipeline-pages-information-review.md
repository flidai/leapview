# Pipeline pages: information review

Text-only wireframes of the implemented pipeline pages as of 23 September 2026. They describe information order, labels, links, and disclosures—not visual styling or real production values. Angle-bracketed text is data; square brackets are controls. A right arrow marks navigation. This document is for reviewing *what each page says* before reviewing how it looks.

## Route map

~~~text
Sidebar: Pipelines
  +-- Pipelines (/pipelines)
  |     +-- <pipeline> -> Overview | Runs | Definition
  +-- Runs (/pipelines/runs) -> <run> -> Execution | Events | Details

Semantic model > Refreshes > linked run ------------------------^
~~~

## 1. Pipelines — What refreshes our data, and does anything need attention?

~~~text
+-----------------------------------------------------------------------+
| Pipelines                                                             |
| [Pipelines*] [Runs]                                                   |
| [Search pipelines...]                                                 |
|                                                                       |
| Pipeline       Refreshes     Schedule      Latest run  Published  Run  |
| <pipeline> ->  <model>       Manual/cron   <status> -> <time>     [▷]  |
|                              Next <time>, when scheduled              |
+-----------------------------------------------------------------------+
~~~

Schedule has its own column; the next scheduled time appears there when recorded. The latest run links to its investigation; last publication describes confirmed data publication, which may differ from the latest run. The icon-only Run now action appears only with permission and retains its accessible label.

## 2. All Runs — What ran, and what happened?

~~~text
+-----------------------------------------------------------------------+
| Runs                                                                  |
| [Pipelines] [Runs*]             [warning if updates reconnect/lost]   |
|                                                                       |
| [Search pipeline or run ID] [Range] [Pipeline] [Status] [Trigger]    |
| [Search] [Clear filters, only when non-default]                        |
|                                                                       |
| Status       Run ID      Started (UTC) -> Pipeline -> Duration Trigger|
| Failed       <short ID>  <time>           <name>       <...>   <...>  |
|                                        <short failure reason>          |
| Succeeded    <short ID>  <time>           <name>       <...>   <...>  |
|                                                                       |
| Showing <start>-<end> of <total> runs    [Previous] [Next]            |
+-----------------------------------------------------------------------+
~~~

There are no metric cards or standalone Error column. Started time is the primary run link. The pipeline name opens its Overview. Row actions, when permitted, can still run or cancel; navigable pipeline runs do not open a duplicate summary drawer. “Prepared” execution is displayed as “Finalizing.”

## 3. Pipeline Overview — What does this pipeline refresh, and when?

~~~text
+-----------------------------------------------------------------------+
| Pipelines > <pipeline>                              [Run now]          |
| <authored description, only if provided>                              |
| [Overview*] [Runs] [Definition]                                        |
|                                                                       |
| Refreshes       <semantic model> ->    Schedule       <cron/Manual>  |
|                                         <timezone> · Next <time>       |
| Latest run      <status · duration · time> ->                          |
| Last published  <publication time> -> / <none or unavailable>        |
|                                                                       |
| Direct dependencies / Full dependency graph                          |
| [Show all upstream / Show direct dependencies] [Fit] [Expand graph]  |
| <dependency graph>                                                    |
| [Dependency list v] -> linked resources, accessible graph alternative|
|                                                                       |
| [Schedule details v] -> cron, timezone, next run, overlap, deadline  |
| [Downstream dashboards · N v] -> potential dashboard links           |
+-----------------------------------------------------------------------+
~~~

Latest-run outcome/time is itself the run link. A confirmed publication time links to its originating run when known. Recent history lives on the adjacent Runs tab, not here. The graph begins with direct dependencies; changing graph scope is separate from Fit. Schedule policy and downstream dashboards are separate disclosures.

## 4. Pipeline Runs — What happened across this pipeline’s executions?

~~~text
+-----------------------------------------------------------------------+
| Pipelines > <pipeline>                              [Run now]          |
| [Overview] [Runs*] [Definition]                                        |
|                                                                       |
| [Search run ID] [Range] [Status] [Trigger] [Search]                   |
| [Clear filters, only when non-default]                                |
|                                                                       |
| Status       Run ID ->       Started (UTC) -> Duration  Trigger        |
| Failed       <short ID>     <time>           <...>     <...>          |
|   <short failure reason>                                             |
|                                                                       |
| Showing <start>-<end> of <total> runs    [Previous] [Next]            |
+-----------------------------------------------------------------------+
~~~

This is the complete run-history destination for one pipeline. It shares the All Runs filtering and pagination pattern, without a pipeline selector or a redundant “Runs” heading.

## 5. Definition — How is this pipeline configured?

~~~text
+-----------------------------------------------------------------------+
| Pipelines > <pipeline>                                                |
| [Overview] [Runs] [Definition*]                                        |
|                                                                       |
| <source/path/pipeline.yaml>                               [Copy]     |
| +-------------------------------------------------------------------+ |
| | <authored YAML for this serving generation, if available>        | |
| +-------------------------------------------------------------------+ |
| [Technical details v] -> Resource ID, content hash                  |
+-----------------------------------------------------------------------+
~~~

The source path replaces the generic “YAML definition” heading. Authored YAML is explicitly unavailable when it cannot be recovered. Technical identifiers remain collapsed.

## 6. Run Execution — What happened in this run, and where?

~~~text
+-----------------------------------------------------------------------+
| Pipelines > [pipeline icon] <pipeline> > Runs > # <short run ID>       |
| <connection warning, only if updates are interrupted>                |
|                                                                       |
| <execution status> · <duration> · <trigger> · <started UTC>            |
| Publication: <published/not published/pending/unverified>            |
|              <snapshot and time, if confirmed>                       |
|                                                                       |
| <run error, if recorded>                       [View failed model]    |
| [Execution*] [Events] [Details]                                        |
|                                                                       |
| Progress                                                              |
| Queue <wait> -> Models <outcome> -> Validation <outcome> -> Publish <phase>|
|                                                                       |
| Dependencies                              Models                       |
| [Show full run graph / Show focused path] <model>   <outcome>          |
| [Fit] [Expand graph]                      <duration, if recorded>      |
| <historical graph / unavailable reason>   <selected model details>   |
|                                           [Open model details] ->      |
+-----------------------------------------------------------------------+
~~~

The execution and publication outcomes stay distinct. Progress contains phase states, not repeated timestamps or explanatory paragraphs. The selected model controls the focused graph and its details; the same model name is not repeated in a graph title and diagnostic heading. An identical run/model error is shown once, near the top. Missing *critical* outcomes are explicit (for example, “Validation: Not recorded”); optional model timing and attempt absence do not generate a stack of repeated notices. On mobile, Models is the default; a Models/Graph switch selects the content, and Progress starts collapsed.

## 7. Events — What happened over time?

~~~text
+-----------------------------------------------------------------------+
| <persistent run outcome summary>                                      |
| [Execution] [Events*] [Details]                                        |
|                                                                       |
| <timestamp UTC>       <recorded event label>                         |
|                       [Technical details v] -> event type, event ID |
| <timestamp UTC>       <recorded event label>                         |
|                       [Technical details v] -> event type, event ID |
+-----------------------------------------------------------------------+
~~~

Only recorded events appear. Event type and ID are available on demand, not repeated beside every readable label. Unavailable, empty, and truncated histories each have a specific state.

## 8. Details — Which invocation and configuration produced this run?

~~~text
+-----------------------------------------------------------------------+
| <persistent run outcome summary>                                      |
| [Execution] [Events] [Details*]                                        |
|                                                                       |
| Invocation                                                            |
|   Trigger; initiator and matching schedule, if known                 |
|   Created; started; finished times                                    |
|                                                                       |
| Attempts (if recorded or explicitly unavailable)                     |
|   Attempt <N> · <outcome> · <duration/error, if recorded>              |
|                                                                       |
| Configuration                                                         |
|   Serving generation; historical definition availability/name       |
|   Plan fingerprint, when recorded                                     |
|                                                                       |
| [Technical identifiers v]                                            |
|   Full pipeline/run/principal/semantic-model IDs [Copy]              |
|   Full plan ID and digests [Copy]                                     |
|   [Recorded model scope · N v] -> model IDs / Inspect execution      |
+-----------------------------------------------------------------------+
~~~

Status, duration, environment, and pipeline name are not repeated in the body. Full identifiers and model scope remain accessible behind disclosures. Historical definition availability is stated honestly.

## 9. Semantic-model Refreshes — Alternate entry to the same run

~~~text
+-----------------------------------------------------------------------+
| Semantic model > <model> > Refreshes                                  |
| Refresh history                                                       |
| Status  Started (UTC)  Run          Duration Trigger Initiated by     |
| <...>   <time>         <short ID> -> <...>    <...>   <principal>      |
+-----------------------------------------------------------------------+
~~~

When a refresh row has a canonical pipeline-run link, both its run link and row action navigate to that investigation. Only refresh records *without* that page retain the legacy detail drawer.

## Review boundaries

- The wireframes are not promises that every data field exists for every historical run.
- A succeeded execution does not imply a confirmed publication.
- The persisted-failure screen has been checked for error placement, deduplication, failed-model selection, and “Not published.” A specific actionable diagnostic *together with* a persisted historical graph remains open in [FAI-1007](https://linear.app/flid/issue/FAI-1007/qualify-persisted-failed-run-investigation-with-historical-graph-and).
