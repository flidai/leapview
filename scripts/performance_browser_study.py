#!/usr/bin/env python3
"""Five fresh maintained browser sessions; no optimization comparison."""
import json
import math
import os
from pathlib import Path
import subprocess
import sys

from performance_capacity import digest, identity, resources, write


METRICS = ("optimisticFeedbackMs", "firstTargetPaintMs", "criticalKPISettlementMs", "allTargetSettlementMs")


def validate_session(report, expected_names):
    if report.get("iterations") != 20:
        raise ValueError("each fresh session requires twenty operations per scenario")
    assertions = report.get("assertions", {})
    if assertions.get("deterministicFailures") != []:
        raise ValueError("browser correctness failure or missing assertions")
    thresholds = assertions.get("thresholds", {})
    if thresholds.get("enabled") is not True or thresholds.get("failures") != []:
        raise ValueError("existing absolute browser guardrails must be enabled and pass")
    samples = report.get("samples", [])
    if len(samples) != len(expected_names) * 20:
        raise ValueError("missing or extra browser samples")
    for name in expected_names:
        selected = [sample for sample in samples if sample.get("interaction") == name]
        if len(selected) != 20 or sorted(sample.get("iteration", 0) for sample in selected) != list(range(1, 21)):
            raise ValueError("wrong or duplicated scenario samples")
        for sample in selected:
            for metric in METRICS:
                value = sample.get(metric)
                if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0:
                    raise ValueError(f"missing or invalid {metric}")
            if not isinstance(sample.get("refreshId"), str) or not sample["refreshId"]:
                raise ValueError("missing refresh identity")
    observation = report.get("observability", {})
    expected = len(set(sample["refreshId"] for sample in samples))
    if observation.get("refreshSummariesExpected") != expected or observation.get("refreshSummariesFound") != expected:
        raise ValueError("missing server-side refresh observations")
    return samples


def run(directory):
    source = identity()
    if not os.environ.get("LEAPVIEW_BASE_URL") or not os.environ.get("LEAPVIEW_PERF_LOG"):
        raise ValueError("explicit running fixture URL and server log are required")
    if any(name.startswith("LEAPVIEW_PERF_MAX_") for name in os.environ):
        raise ValueError("study preserves maintained thresholds; override variables are not accepted")
    scenario = Path(os.environ.get("LEAPVIEW_PERF_SCENARIO", "scripts/performance/movielens.json"))
    names = [item["name"] for item in json.loads(scenario.read_text())["scenarios"]]
    if not names or len(set(names)) != len(names):
        raise ValueError("scenario names must be unique and nonempty")
    directory.mkdir(parents=True, exist_ok=False)
    write(directory / "protocol.json", {"source": source, "freshBrowserSessions": 5,
        "operationsPerScenarioPerSession": 20, "scenarioSHA256": digest(scenario),
        "command": ["bun", "scripts/dashboard_performance.ts"], "resources": resources(),
        "primary": "allTargetSettlementMs", "guardrails": list(METRICS),
        "stop": "first process, correctness, guardrail, timeout, missing sample or source-drift failure",
        "decision": "current-source characterization only; no optimization adoption or comparison",
        "session": "new browser process and page each time; the fixed server remains warm",
        "serverURL": os.environ["LEAPVIEW_BASE_URL"]})
    sessions = []
    failure = None
    try:
        for session in range(1, 6):
            if identity() != source:
                raise ValueError("source changed during browser study")
            report = directory / f"session-{session}.json"
            env = {**os.environ, "LEAPVIEW_PERF_ITERATIONS": "20", "LEAPVIEW_PERF_ENFORCE_THRESHOLDS": "1",
                   "LEAPVIEW_PERF_OUTPUT": str(report)}
            with (directory / f"session-{session}.log").open("xb") as log:
                subprocess.run(["bun", "scripts/dashboard_performance.ts"], env=env,
                               stdout=log, stderr=subprocess.STDOUT, timeout=1200, check=True)
            samples = validate_session(json.loads(report.read_text()), names)
            sessions.append({"session": session, "reportSHA256": digest(report), "samples": len(samples)})
        if identity() != source:
            raise ValueError("source changed before final browser receipt")
    except (ValueError, subprocess.SubprocessError) as error:
        failure = str(error)
    write(directory / "decision.json", {"source": source, "result": "complete" if failure is None else "stopped",
        "failure": failure, "sessions": sessions, "adoption": "none",
        "limitation": "fixed browser workload on a warm local server; fixture/build admission and other dense/scroll/map workloads need separate evidence"})
    return 0 if failure is None else 1


if __name__ == "__main__":
    if len(sys.argv) != 2 or not Path(sys.argv[1]).is_absolute():
        sys.exit("usage: performance_browser_study.py NEW_ABSOLUTE_EVIDENCE_DIR")
    sys.exit(run(Path(sys.argv[1])))
