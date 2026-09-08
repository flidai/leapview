import "../exploration/main.tsp";

using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Analytics;

/**
 * The immutable revision identity returned by saved-exploration reads. The
 * complete token is also the value accepted in If-Match for mutations; it is
 * never inferred from a title, slug, or payload hash alone.
 */
model SavedExplorationRevisionToken {
  revisionId: string;
  @minValue(1)
  number: int64;
  contentHash: string;
}

enum SavedExplorationVisibility {
  private;
  organization;
}

enum SavedExplorationStatus {
  active;
  archived;
}

model SavedExplorationSummaryResponse {
  id: ResourceId;
  ownerPrincipalId: string;
  title: string;
  slug: string;
  visibility: SavedExplorationVisibility;
  semanticModelId: ResourceId;
  status: SavedExplorationStatus;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
  archivedAt?: utcDateTime;
  revision: SavedExplorationRevisionToken;
}

/**
 * GET is the reopen operation: it returns a detached, editable authored
 * working copy and the exact source revision token used by PATCH.
 */
model SavedExplorationResponse extends SavedExplorationSummaryResponse {
  spec: LeapViewExploration.ExplorationSpec;
}

model SavedExplorationListResponse {
  items: SavedExplorationSummaryResponse[];
  page: PageInfo;
}

// Mutation requests intentionally contain authored intent only. Actor,
// owner, revision evidence, request IDs, and correlation IDs are all bound by
// authenticated transport and the command runtime on the server.
model SavedExplorationCreateRequest {
  title: string;
  slug?: string;
  visibility: SavedExplorationVisibility;
  spec: LeapViewExploration.ExplorationSpec;
}

model SavedExplorationPatchRequest {
  title: string;
  slug: string;
  visibility: SavedExplorationVisibility;
  spec: LeapViewExploration.ExplorationSpec;
}

model SavedExplorationDuplicateRequest {
  title?: string;
  slug?: string;
  visibility?: SavedExplorationVisibility;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model SavedExplorationMutationAuditPayload {
  @apigen.auditInternal
  mutationEvidenceVersion: uint32;
  @apigen.auditInternal
  actorId: string;
  @apigen.auditInternal
  action: string;
  @apigen.auditInternal
  idempotencyKey: string;
  @apigen.auditInternal
  fingerprint: string;
  @apigen.auditInternal
  requestId: string;
  @apigen.auditInternal
  correlationId: string;
  @apigen.auditInternal
  adminOverride: boolean;
  @apigen.auditInternal
  adminReason: string;
  @apigen.auditInternal
  occurredAt: utcDateTime;
  @apigen.auditInternal
  appliedRevision: SavedExplorationRevisionToken;
}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 422, code: "INVALID_SAVED_EXPLORATION", publicDetail: "The saved-exploration request is invalid." })
model InvalidSavedExplorationFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "SAVED_EXPLORATION_NOT_FOUND", publicDetail: "Saved exploration not found." })
model SavedExplorationNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "SAVED_EXPLORATION_CONFLICT", publicDetail: "The saved exploration conflicts with its current state." })
model SavedExplorationConflictFailure {}

@apigen.failureDefinition(#{ kind: "precondition", statusCode: 412, code: "SAVED_EXPLORATION_PRECONDITION_FAILED", publicDetail: "The saved exploration changed before this request was applied." })
model SavedExplorationPreconditionFailure {}

@apigen.failureDefinition(#{ kind: "unavailable", statusCode: 503, code: "SAVED_EXPLORATION_UNAVAILABLE", publicDetail: "Saved-exploration service is unavailable." })
model SavedExplorationUnavailableFailure {}

// Saved-exploration mutations and working-copy reads return the complete
// revision token as an HTTP ETag. Keep the header in the response contract so
// generated servers and clients cannot silently drop the concurrency token.
model SavedExplorationETagOK<T> {
  ...OkResponse;
  @header("ETag") etag: string;
  ...Body<T>;
}

model SavedExplorationETagCreated<T> {
  ...CreatedResponse;
  @header location: string;
  @header("ETag") etag: string;
  ...Body<T>;
}

alias ListSavedExplorationsOK = OkJson<SavedExplorationListResponse>;
alias GetSavedExplorationOK = SavedExplorationETagOK<SavedExplorationResponse>;
alias CreateSavedExplorationCreated = SavedExplorationETagCreated<SavedExplorationResponse>;
alias UpdateSavedExplorationOK = SavedExplorationETagOK<SavedExplorationResponse>;
alias DuplicateSavedExplorationCreated = SavedExplorationETagCreated<SavedExplorationResponse>;
alias ArchiveSavedExplorationOK = SavedExplorationETagOK<SavedExplorationSummaryResponse>;

@route("/projects/{project}/saved-explorations")
@tag("Saved Explorations")
@apigen.commandDefaults(#{ guarantee: "transactional" })
interface SavedExplorations {
  @summary("List saved explorations")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  listSavedExplorations(@path project: ResourceId, @query includeArchived?: boolean, @query limit?: ListLimit, @query pageToken?: PageToken): ListSavedExplorationsOK | CommonErrors;

  @summary("Create a saved exploration")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.failsWith(InvalidSavedExplorationFailure)
  @apigen.failsWith(SavedExplorationNotFoundFailure)
  @apigen.failsWith(SavedExplorationConflictFailure)
  @apigen.failsWith(SavedExplorationUnavailableFailure)
  @apigen.auditPayload(SavedExplorationMutationAuditPayload)
  @apigen.command(#{ auditAction: "saved_exploration.created" })
  @apigen.ui("saved-exploration.create")
  createSavedExploration(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: SavedExplorationCreateRequest): CreateSavedExplorationCreated | CommonErrors;

  @summary("Get a saved exploration working copy")
  @route("/{exploration}")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  getSavedExploration(@path project: ResourceId, @path exploration: ResourceId): GetSavedExplorationOK | CommonErrors;

  @summary("Update a saved exploration")
  @route("/{exploration}")
  @patch
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.failsWith(InvalidSavedExplorationFailure)
  @apigen.failsWith(SavedExplorationNotFoundFailure)
  @apigen.failsWith(SavedExplorationConflictFailure)
  @apigen.failsWith(SavedExplorationPreconditionFailure)
  @apigen.failsWith(SavedExplorationUnavailableFailure)
  @apigen.auditPayload(SavedExplorationMutationAuditPayload)
  @apigen.command(#{ auditAction: "saved_exploration.updated" })
  @apigen.ui("saved-exploration.update")
  updateSavedExploration(@path @apigen.target project: ResourceId, @path exploration: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @header("If-Match") ifMatch: string, @body body: SavedExplorationPatchRequest): UpdateSavedExplorationOK | CommonErrors;

  @summary("Duplicate a saved exploration")
  @route("/{exploration}/duplicate")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.failsWith(InvalidSavedExplorationFailure)
  @apigen.failsWith(SavedExplorationNotFoundFailure)
  @apigen.failsWith(SavedExplorationConflictFailure)
  @apigen.failsWith(SavedExplorationPreconditionFailure)
  @apigen.failsWith(SavedExplorationUnavailableFailure)
  @apigen.auditPayload(SavedExplorationMutationAuditPayload)
  @apigen.command(#{ auditAction: "saved_exploration.duplicated" })
  @apigen.ui("saved-exploration.duplicate")
  duplicateSavedExploration(@path @apigen.target project: ResourceId, @path exploration: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @header("If-Match") ifMatch: string, @body body: SavedExplorationDuplicateRequest): DuplicateSavedExplorationCreated | CommonErrors;

  @summary("Archive a saved exploration")
  @route("/{exploration}/archive")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.failsWith(InvalidSavedExplorationFailure)
  @apigen.failsWith(SavedExplorationNotFoundFailure)
  @apigen.failsWith(SavedExplorationConflictFailure)
  @apigen.failsWith(SavedExplorationPreconditionFailure)
  @apigen.failsWith(SavedExplorationUnavailableFailure)
  @apigen.auditPayload(SavedExplorationMutationAuditPayload)
  @apigen.command(#{ auditAction: "saved_exploration.archived" })
  @apigen.ui("saved-exploration.archive")
  archiveSavedExploration(@path @apigen.target project: ResourceId, @path exploration: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @header("If-Match") ifMatch: string): ArchiveSavedExplorationOK | CommonErrors;
}
