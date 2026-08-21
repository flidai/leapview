import "@yacobolo/apigen";

@apigen.`package`(#{
  title: "LeapView Pipeline Document",
  version: "1.0.0",
  description: "The canonical asset-selected refresh Pipeline resource document.",
})
namespace LeapViewPipeline;

@minLength(1)
@maxLength(128)
@pattern("^[A-Za-z0-9][A-Za-z0-9_.:-]*$")
scalar PipelineResourceID extends string;

@minLength(1)
@maxLength(128)
@pattern("^[A-Za-z_][A-Za-z0-9_.-]*$")
scalar PipelineResourceName extends string;

/** Evidence-only schedule key. Its spelling has no scheduling semantics. */
@minLength(1)
scalar PipelineScheduleID extends string;

/** A five-field Argo-compatible cron expression supplied by an author. */
@minLength(1)
scalar PipelineCronExpression extends string;

/** Explicit IANA timezone used for every schedule in the Pipeline. */
@minLength(1)
scalar PipelineTimezone extends string;

enum PipelineApiVersion {
  v1: "leapview.dev/v1",
}

enum PipelineResourceKind {
  pipeline: "Pipeline",
}

enum PipelineConcurrencyPolicy {
  forbid: "Forbid",
  replace: "Replace",
}

model PipelineMetadata {
  id: PipelineResourceID;
  name: PipelineResourceName;
  displayName?: string;
  description?: string;
  owner?: string;
  domain?: string;
  tags?: string[];
  documentation?: string;
}

model PipelineSelection {
  semanticModel: PipelineResourceName;
}

/** Manual-only form: schedule policy is not meaningful without schedules. */
model ManualPipelineSpec {
  selection: PipelineSelection;
}

/** Scheduled form: all Pipeline-wide schedule policy is explicit. */
model ScheduledPipelineSpec {
  selection: PipelineSelection;
  /** Named schedule IDs are evidence-only keys; values are cron expressions. */
  @apigen.minProperties(1)
  @apigen.propertyNames(PipelineScheduleID)
  schedules: Record<PipelineCronExpression>;
  /** Required when schedules is non-empty; omitted for manual-only Pipelines. */
  timezone: PipelineTimezone;
  /** Argo-compatible late-start deadline, required when schedules is non-empty. */
  @minValue(0)
  startingDeadlineSeconds: int64;
  /** Argo-compatible scheduled overlap policy, required when schedules is non-empty. */
  concurrencyPolicy: PipelineConcurrencyPolicy;
}

/** Conditional authored shape; the union is structural and has no YAML tag. */
union PipelineSpec {
  manual: ManualPipelineSpec,
  scheduled: ScheduledPipelineSpec,
}

@apigen.contract(#{ kind: "pipeline", tags: #["pipeline", "authoring", "resource"] })
model PipelineDocument {
  apiVersion: PipelineApiVersion;
  kind: PipelineResourceKind;
  metadata: PipelineMetadata;
  spec: PipelineSpec;
}
