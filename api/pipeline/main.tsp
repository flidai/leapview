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

@minLength(1)
@maxLength(128)
@pattern("^[A-Za-z_][A-Za-z0-9_-]*$")
scalar PipelineIdentifier extends string;

enum PipelineApiVersion {
  v1: "leapview.dev/v1",
}

enum PipelineResourceKind {
  pipeline: "Pipeline",
}

enum PipelineMissedOccurrences {
  skip: "skip",
  latest: "latest",
}

enum PipelineOverlapPolicy {
  forbid: "forbid",
  replace: "replace",
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

@discriminator("type")
model PipelineSelection {
  type: string;
}

model SemanticModelPipelineSelection extends PipelineSelection {
  type: "semanticModel";
  semanticModel: PipelineResourceID;
}

@discriminator("type")
model PipelineTrigger {
  id: PipelineIdentifier;
  type: string;
}

model ManualPipelineTrigger extends PipelineTrigger {
  id: PipelineIdentifier;
  type: "manual";
}

model SchedulePipelineTrigger extends PipelineTrigger {
  id: PipelineIdentifier;
  type: "schedule";
  cron: string;
  timezone: string;
  missedOccurrences: PipelineMissedOccurrences;
}

model PipelineRunPolicy {
  overlap: PipelineOverlapPolicy;
}

model PipelineSpec {
  selection: PipelineSelection;
  @minItems(1)
  triggers: PipelineTrigger[];
  runPolicy: PipelineRunPolicy;
}

@apigen.contract(#{ kind: "pipeline", tags: #["pipeline", "authoring", "resource"] })
model PipelineDocument {
  apiVersion: PipelineApiVersion;
  kind: PipelineResourceKind;
  metadata: PipelineMetadata;
  spec: PipelineSpec;
}
