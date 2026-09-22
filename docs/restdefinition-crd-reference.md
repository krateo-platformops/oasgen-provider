---
type: CRD
title: RestDefinition CRD reference
description: Generated field-by-field reference for restdefinitions.ogen.krateo.io (crdoc output from go/oasgen-provider/crds; regenerate after `make generate`).
resource: restdefinitions.ogen.krateo.io
tags: [kog, crd, restdefinition, generated]
timestamp: 2026-09-22T00:00:00Z
---

# API Reference

Packages:

- [ogen.krateo.io/v1alpha1](#ogenkrateoiov1alpha1)

# ogen.krateo.io/v1alpha1

Resource Types:

- [RestDefinition](#restdefinition)




## RestDefinition
<sup><sup>[↩ Parent](#ogenkrateoiov1alpha1 )</sup></sup>






RestDefinition is a RestDefinition type with a spec and a status.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
      <td><b>apiVersion</b></td>
      <td>string</td>
      <td>ogen.krateo.io/v1alpha1</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b>kind</b></td>
      <td>string</td>
      <td>RestDefinition</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta">metadata</a></b></td>
      <td>object</td>
      <td>Refer to the Kubernetes API documentation for the fields of the `metadata` field.</td>
      <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspec">spec</a></b></td>
        <td>object</td>
        <td>
          RestDefinitionSpec is the specification of a RestDefinition.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionstatus">status</a></b></td>
        <td>object</td>
        <td>
          RestDefinitionStatus is the status of a RestDefinition.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec
<sup><sup>[↩ Parent](#restdefinition)</sup></sup>



RestDefinitionSpec is the specification of a RestDefinition.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>oasPath</b></td>
        <td>string</td>
        <td>
          Path to the OpenAPI specification. This value can change over time, for example if the OAS file is updated but be sure to not change the requestbody of the `create` verb.
- configmap://<namespace>/<name>/<key>
- http(s)://<url><br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresource">resource</a></b></td>
        <td>object</td>
        <td>
          The resource to manage<br/>
          <br/>
            <i>Validations</i>:<li>!(has(self.createApiRef) && has(self.observeApiRef) && !has(self.observeApiRef.notFoundExpr)): createApiRef with observeApiRef requires observeApiRef.notFoundExpr, so a create can be triggered when the delegated observe reports the resource absent</li><li>!has(self.createApiRef) || self.verbsDescription.exists(v, v.action == 'get' || v.action == 'findby'): createApiRef requires a get or findby verb so the controller can verify the create converged (level-based convergence)</li><li>!has(self.compareScope) || self.compareScope != 'identifiersAndStatus' || (has(self.identifiers) && size(self.identifiers) > 0) || (has(self.additionalStatusFields) && size(self.additionalStatusFields) > 0): compareScope 'identifiersAndStatus' requires at least one identifier or additionalStatusField to compare against</li><li>!has(self.compareScope) || self.compareScope != 'updatable' || self.verbsDescription.exists(v, v.action == 'update'): compareScope 'updatable' requires an update verb: with no update verb the set of updatable fields is empty, so nothing would ever be compared and the resource would silently never report drift</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>resourceGroup</b></td>
        <td>string</td>
        <td>
          Group: the group of the resource to manage<br/>
          <br/>
            <i>Validations</i>:<li>self == oldSelf: ResourceGroup is immutable, you cannot change that once the CRD has been generated</li>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource
<sup><sup>[↩ Parent](#restdefinitionspec)</sup></sup>



The resource to manage

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>kind</b></td>
        <td>string</td>
        <td>
          Name: the name of the resource to manage<br/>
          <br/>
            <i>Validations</i>:<li>self == oldSelf: Kind is immutable, you cannot change that once the CRD has been generated</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindex">verbsDescription</a></b></td>
        <td>[]object</td>
        <td>
          VerbsDescription: the list of verbs to use on this resource<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>additionalStatusFields</b></td>
        <td>[]string</td>
        <td>
          AdditionalStatusFields: the list of fields to use as additional status fields - used to populate the status of the resource<br/>
          <br/>
            <i>Validations</i>:<li>self == oldSelf: AdditionalStatusFields are immutable, you cannot change them once the CRD has been generated</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>compareScope</b></td>
        <td>enum</td>
        <td>
          CompareScope selects which fields the drift comparison (Observe) considers when deciding whether the
external resource is up to date.
  - "fullSpec" (default, also when unset): every spec field is compared against the observed response.
  - "identifiersAndStatus": only the fields listed in identifiers + additionalStatusFields are compared.
    Fields outside that set no longer trigger updates, so use this ONLY when those fields capture
    everything worth reconciling (e.g. all other spec fields are create-only / server-managed). It trades
    precision for ergonomics: no per-field responseTransform/fieldMapping is needed to stop false drift on
    divergently-shaped response fields. Being reconcile behavior rather than CRD shape, it is mutable.
  - "updatable": only the fields the UPDATE verb's request body can actually express are compared,
    derived from the OAS rather than declared by hand. A field the update cannot send is a field the
    controller cannot fix, so comparing it can only produce an update that changes nothing and a
    difference that returns on the next reconcile. This sits between "fullSpec" (which loops on
    server-assigned or create-only fields) and "identifiersAndStatus" (which stops comparing almost
    everything, including drift that IS fixable). Requires an update verb — see the validation below.<br/>
          <br/>
            <i>Enum</i>: fullSpec, identifiersAndStatus, updatable<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceconfigurationfieldsindex">configurationFields</a></b></td>
        <td>[]object</td>
        <td>
          ConfigurationFields: the list of fields to use as configuration fields<br/>
          <br/>
            <i>Validations</i>:<li>self == oldSelf: ConfigurationFields are immutable, you cannot change them once the CRD has been generated</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourcecreateapiref">createApiRef</a></b></td>
        <td>object</td>
        <td>
          CreateApiRef, when set, delegates CREATE of this resource to a Snowplow RESTAction instead of the
create verb: the controller invokes the referenced RESTAction (passing the resource's name/namespace/
uid and its whole spec — the desired state — as request extras) to run the multi-call provisioning
sequence, and projects any composed .status it returns into this resource's status. The RESTAction
MUST be idempotent: the controller does not verify per-call success — it re-invokes create every
reconcile until Observe reports the resource exists (level-based convergence). This therefore REQUIRES
a get/findby verb — OR an observeApiRef whose notFoundExpr reports non-existence — the observe that
reports non-existence; with none, the resource is marked Available after a single unverified
invocation. Dissolves proxies whose only job is to chain create calls (e.g. create instance -> attach
disk -> start).<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourcedeleteapiref">deleteApiRef</a></b></td>
        <td>object</td>
        <td>
          DeleteApiRef, when set, delegates DELETE of this resource to a Snowplow RESTAction instead of the
delete verb: on deletion the controller invokes the referenced RESTAction (the teardown sequence) and
holds the finalizer until it succeeds. The RESTAction MUST be idempotent and tolerate an already-gone
sub-resource.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>excludedSpecFields</b></td>
        <td>[]string</td>
        <td>
          ExcludedSpecFields: the list of fields to exclude from the spec of the generated CRD (for example server-generated technical IDs could be excluded)<br/>
          <br/>
            <i>Validations</i>:<li>self == oldSelf: ExcludedSpecFields are immutable, you cannot change them once the CRD has been generated</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>identifiers</b></td>
        <td>[]string</td>
        <td>
          Identifiers: the list of fields to use as identifiers - used to populate the status of the resource<br/>
          <br/>
            <i>Validations</i>:<li>self == oldSelf: Identifiers are immutable, you cannot change them once the CRD has been generated</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceobserveapiref">observeApiRef</a></b></td>
        <td>object</td>
        <td>
          ObserveApiRef, when set, delegates the OBSERVE of this resource to a Snowplow RESTAction instead of the
get/findby verbs: the rest-dynamic-controller invokes the referenced RESTAction (via snowplow /call,
under its own identity) each reconcile — passing the resource's name/namespace/uid and its identifier
values (not the whole spec) as request extras — and projects the RESTAction's composed .status into
this resource's status (leaving the runtime-managed conditions untouched). It dissolves proxies whose
only job is to compose a multi-call observation (read several sub-resources and shape one status). The
referenced RESTAction is trusted platform configuration. Being reconcile behavior rather than CRD
shape, it is intentionally mutable.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceupdateapiref">updateApiRef</a></b></td>
        <td>object</td>
        <td>
          UpdateApiRef, when set, delegates UPDATE of this resource to a Snowplow RESTAction instead of the
update verb: when Observe reports drift the controller invokes the referenced RESTAction (passing the
whole spec — the desired state) to re-apply it. Like create, the RESTAction MUST be idempotent.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index]
<sup><sup>[↩ Parent](#restdefinitionspecresource)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>action</b></td>
        <td>enum</td>
        <td>
          Name of the action to perform when this api is called [create, update, get, delete, findby]<br/>
          <br/>
            <i>Enum</i>: create, update, get, delete, findby<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>method</b></td>
        <td>enum</td>
        <td>
          Method: the http method to use [GET, POST, PUT, DELETE, PATCH]<br/>
          <br/>
            <i>Enum</i>: GET, POST, PUT, DELETE, PATCH<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>path</b></td>
        <td>string</td>
        <td>
          Path: the path to the api - has to be the same path as the one in the OAS file you are referencing<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexasync">async</a></b></td>
        <td>object</td>
        <td>
          Async declares long-running-operation (async) handling for this mutating verb: after the trigger call
returns an operation handle, the controller polls an operations endpoint until it reaches a terminal
status, turning an asynchronous API into a synchronous reconcile. Set only on create/update/delete.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexfieldmappingindex">fieldMapping</a></b></td>
        <td>[]object</td>
        <td>
          FieldMapping provides unified request/response value relocation and optional per-field value
transforms (alias or jq). It supersedes RequestFieldMapping: request entries (inPath/inQuery/inBody)
behave as before, and response entries (inResponse) normalize the observed body into the CR-domain
shape at the reconcile chokepoint, before status population and drift comparison. Being reconcile
behavior rather than CRD shape, it is intentionally mutable.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexheadersindex">headers</a></b></td>
        <td>[]object</td>
        <td>
          Headers is a list of static HTTP headers to inject on every request for this verb, e.g. an API that
requires a specific 'Accept' media-type or 'Content-Type' the OAS does not otherwise enforce. Header
values are sent verbatim and are not validated against the OAS.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>identifiersMatchPolicy</b></td>
        <td>enum</td>
        <td>
          IdentifiersMatchPolicy defines how to match identifiers for the 'findby' action. To be set only for 'findby' actions.
If not set, defaults to 'OR'.
Possible values are 'AND' or 'OR'.
- 'AND': all identifiers must match.
- 'OR': at least one identifier must match (the default behavior).<br/>
          <br/>
            <i>Enum</i>: AND, OR<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexnotfoundbody">notFoundBody</a></b></td>
        <td>object</td>
        <td>
          NotFoundBody is the body-based counterpart of NotFoundCodes: a gojq predicate evaluated against the
successful (2xx) observe-response. When it yields true the external resource is treated as NOT
existing, so the reconciler creates it. Use it for APIs that signal absence with a 200 body rather
than a status code. The program must return a boolean, and its input '.' is the RAW body, whose shape
differs by verb:
  - get:    the whole GET body — e.g. `.items | length == 0`, `.deleted == true`, `.status == "NOT_FOUND"`.
  - findby: the SINGLE matched item (a findby no-match already yields not-found on its own), so write
            it against the item — e.g. a tombstone `.status == "deleted"`; a list-shaped predicate is
            meaningless here.
Intended for get/findby only. It is not evaluated while the resource is Pending (mid async create), and
a non-boolean result is a reconcile error.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>notFoundCodes</b></td>
        <td>[]integer</td>
        <td>
          NotFoundCodes lists HTTP status codes that, for this verb, mean the external resource does NOT exist
(i.e. are remapped to a not-found result) even though they are not 404. Use it for APIs that signal
absence with a non-standard code the reconciler would otherwise treat as an error or as existing —
e.g. an existence check that returns 410 Gone or 204 for a missing resource. Intended for get/findby.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>oasPath</b></td>
        <td>string</td>
        <td>
          OASPath optionally overrides spec.oasPath for THIS VERB ONLY, using the same URI schemes:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url>

A resource whose verbs span two OAS documents is otherwise undescribable, because spec.oasPath is
singular. That is not a degraded case but an impossible one: when a vendor introduces an operation
in a later API version, the resource cannot be expressed at all. Aruba's CloudServer is the
motivating example -- findby/get/delete live in compute-provider.json (1.0.0) while create lives in
compute-provider_v1.1.json (1.1.0), and Aruba's own SDK pins exactly that split.

Documents are read as published and never merged: pre-merging would break the guarantee that a
generated CRD traces back to one vendor document, which this repo checksum-enforces. Where two
documents disagree about a schema both use, the RestDefinition is REJECTED at admission rather
than silently preferring one.

Which document supplies what is unchanged, only made explicit: the create verb's document drives
the generated spec schema, the observe (get/findby) document drives status and identifiers, and
security schemes come from the resource-level document.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexpagination">pagination</a></b></td>
        <td>object</td>
        <td>
          Pagination defines the pagination strategy for 'findby' actions. To be set only for 'findby' actions.
If not set, no pagination will be used.<br/>
          <br/>
            <i>Validations</i>:<li>self.type == 'continuationToken' ? has(self.continuationToken) : true: continuationToken configuration must be provided when type is 'continuationToken'</li><li>self.type == 'pageNumber' ? has(self.pageNumber) : true: pageNumber configuration must be provided when type is 'pageNumber'</li><li>self.type == 'continuationToken' ? !has(self.pageNumber) : true: pageNumber must not be set when type is 'continuationToken'</li><li>self.type == 'pageNumber' ? !has(self.continuationToken) : true: continuationToken must not be set when type is 'pageNumber'</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexqueriesindex">queries</a></b></td>
        <td>[]object</td>
        <td>
          Queries is a list of static query parameters to inject on every request for this verb, e.g. an
API that requires a specific 'api-version' the CRD-generating path does not carry. Combined with the
per-verb path/method and request fieldMapping, this expresses an alternative endpoint for a verb
(e.g. an update routed to a different sub-API with its own api-version). Values are sent verbatim.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexrequestfieldmappingindex">requestFieldMapping</a></b></td>
        <td>[]object</td>
        <td>
          RequestFieldMapping provides explicit mapping from API parameters (path, query, or body)
to fields in the Custom Resource.

Deprecated: use FieldMapping instead. RequestFieldMapping is request-direction only and carries no
value transform; it is retained for backward compatibility and each entry is treated as an
equivalent request-direction FieldMappingItem at load time. It will be removed after a migration window.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexrequesttransform">requestTransform</a></b></td>
        <td>object</td>
        <td>
          RequestTransform is a whole-document gojq program applied to the assembled request body immediately
before it is sent — the document-scoped sibling of a per-field jq valueMapping, mirroring
ResponseTransform. Input '.' is the entire body and the single output replaces it.

It runs AFTER the per-field fieldMapping entries have composed the body, so the program sees the
finished article. That is the inverse of ResponseTransform, which runs BEFORE per-field mapping —
both orderings exist so the document program always gets the API-shaped view.

Applies only where a body is sent (create/update/delete); on a bodyless verb it is a no-op rather
than an invented body. A program that fails to compile or run fails the reconcile rather than
sending a partially transformed body.

Requires rest-dynamic-controller >= 0.17.0. Between oasgen 0.16.0 and this release the field was
REJECTED at admission: it was accepted by the schema and executed by nothing, and rejecting it was
preferable to letting it be a silent no-op.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexresponsetransform">responseTransform</a></b></td>
        <td>object</td>
        <td>
          ResponseTransform is a whole-document gojq program applied to the raw response body once, at the
reconcile chokepoint, before per-field fieldMapping, status population and drift comparison. Input
'.' is the entire response body and the single output replaces it. It is the declarative form of a
plugin's whole-body response normalizer.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>successCodes</b></td>
        <td>[]integer</td>
        <td>
          SuccessCodes lists additional HTTP status codes to treat as success for this verb, beyond the 2xx
codes declared for the operation in the OAS document. Use it when an API returns a non-standard
success code the OAS does not document (e.g. a 201 or 202 that the reconciler would otherwise reject
as an invalid status). Values are merged with the OAS-derived success codes, never replacing them.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>tolerateCodes</b></td>
        <td>[]integer</td>
        <td>
          TolerateCodes lists HTTP status codes that, for this verb, are treated as a successful EMPTY response
instead of an error. Use it when a code that would otherwise fail the call actually means "the
(sub-)resource is simply empty / not present yet" rather than a real failure — e.g. an API returning
404 for an optional collection with no entries. Use with care: tolerating 404 on a verb whose code
genuinely signals a deleted resource would mask that deletion.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].async
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindex)</sup></sup>



Async declares long-running-operation (async) handling for this mutating verb: after the trigger call
returns an operation handle, the controller polls an operations endpoint until it reaches a terminal
status, turning an asynchronous API into a synchronous reconcile. Set only on create/update/delete.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexasyncoperationref">operationRef</a></b></td>
        <td>object</td>
        <td>
          OperationRef: how to extract the operation handle from the trigger response.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexasyncpoll">poll</a></b></td>
        <td>object</td>
        <td>
          Poll: the polling endpoint and its terminal semantics.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>mode</b></td>
        <td>enum</td>
        <td>
          Mode selects how the long-running operation is driven:
  - "blocking" (default): the trigger reconcile polls the operation to completion inline (Model A).
    Simplest, but occupies a reconcile worker for the duration of the operation.
  - "requeue": the trigger reconcile fires the operation, records its handle, and returns; each
    subsequent reconcile polls the operation once and requeues until it reaches a terminal status
    (Model B). Non-blocking — it never pins a worker — and adds terminal-failure detection to the
    otherwise blind "wait for the resource to appear" pending flow. Because the operation is triggered
    before its handle is recorded, the trigger must be idempotent (as for blocking mode). requeue
    applies to create/update; delete always polls inline, since the finalizer must be held until the
    delete operation completes.<br/>
          <br/>
            <i>Enum</i>: blocking, requeue<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>postGet</b></td>
        <td>boolean</td>
        <td>
          PostGet: after terminal success, re-run the resource's get/findby to fetch the final state.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].async.operationRef
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexasync)</sup></sup>



OperationRef: how to extract the operation handle from the trigger response.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>in</b></td>
        <td>enum</td>
        <td>
          In: where the handle is located: "body" (a JSONPath into the trigger response body) or "header".<br/>
          <br/>
            <i>Enum</i>: body, header<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>path</b></td>
        <td>string</td>
        <td>
          Path: the JSONPath (for in=body) or header name (for in=header) that carries the handle.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexasyncoperationrefjq">jq</a></b></td>
        <td>object</td>
        <td>
          JQ: optional gojq program to derive the handle from the raw value at Path (e.g. an id from a URL).<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].async.operationRef.jq
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexasyncoperationref)</sup></sup>



JQ: optional gojq program to derive the handle from the raw value at Path (e.g. an id from a URL).

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].async.poll
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexasync)</sup></sup>



Poll: the polling endpoint and its terminal semantics.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>path</b></td>
        <td>string</td>
        <td>
          Path: the poll endpoint template. Two things must hold together, and both are checked when the
RestDefinition is processed rather than left to fail on the first poll:

 1. it must contain the literal {operationId} token — that is the parameter name the extracted handle
    is bound to in rest-dynamic-controller;
 2. it must be an EXACT key of the OAS paths object, because paths are resolved by exact string
    lookup.

Together they mean the poll endpoint must be declared in the OAS document with a path parameter whose
name matches handleParam below.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>statusPath</b></td>
        <td>string</td>
        <td>
          StatusPath: JSONPath to the status field in the poll response.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>successValues</b></td>
        <td>[]string</td>
        <td>
          SuccessValues: status values that mark terminal success.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>failureValues</b></td>
        <td>[]string</td>
        <td>
          FailureValues: status values that mark terminal failure.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>handleParam</b></td>
        <td>string</td>
        <td>
          HandleParam is the NAME of the path parameter in Path that receives the extracted operation handle.
Defaults to "operationId".

Despite that default's spelling this is NOT the OAS `operationId` keyword: that identifies an
operation definition and is optional, whereas this names a path parameter. An async API is not
required to expose anything called operationId — it only has to return a handle somewhere in the
trigger response (declared by operationRef) and offer a poll endpoint that accepts it.

Set this when the OAS document names the parameter something else, which vendor specs routinely do:
Aruba's baremetal API declares .../monitor/{id}, so path: .../monitor/{id} with handleParam: id uses
that document unmodified. Before this field existed the document had to be patched to rename the
parameter to operationId.

Requires rest-dynamic-controller >= 0.18.0; an older controller ignores it and binds to operationId.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>intervalSeconds</b></td>
        <td>integer</td>
        <td>
          IntervalSeconds: delay between polls. Defaults to 1.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>maxAttempts</b></td>
        <td>integer</td>
        <td>
          MaxAttempts: maximum number of poll attempts. Defaults to 10.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>method</b></td>
        <td>enum</td>
        <td>
          Method: the HTTP method for the poll call (GET in practice).<br/>
          <br/>
            <i>Enum</i>: GET<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>timeoutSeconds</b></td>
        <td>integer</td>
        <td>
          TimeoutSeconds: overall cap on the polling loop. 0 means no explicit cap (bounded by MaxAttempts).<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].fieldMapping[index]
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindex)</sup></sup>



FieldMappingItem defines a single unified mapping entry: it relocates a value between the Custom
Resource and the external API (request OR response direction) and optionally transforms the value as
it crosses that boundary. It generalizes RequestFieldMappingItem (which is retained, deprecated, for
backward compatibility): the request anchors inPath/inQuery/inBody keep their existing meaning, and a
new inResponse anchor selects a field of the API response body to be normalized into the CR-facing
shape before status population and drift comparison.

Exactly one API-side anchor must be set. The anchor kind implies the direction:
inPath/inQuery/inBody => request, inResponse => response.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>defaultIfAbsent</b></td>
        <td>JSON</td>
        <td>
          DefaultIfAbsent, for a response entry (inResponse), supplies the value to inject at the CR-domain
destination when the API omits the source field entirely. This canonicalizes an absent field into a
known default so status population and drift comparison converge — e.g. an API that omits a boolean
object when it is false. It is an arbitrary JSON value (scalar, object or array). Ignored when the
source field is present, and ignored for request-direction entries.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inBody</b></td>
        <td>string</td>
        <td>
          InBody selects a REQUEST body field (request direction).
Only one of 'inPath', 'inQuery', 'inBody' or 'inResponse' can be set.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inCustomResource</b></td>
        <td>string</td>
        <td>
          InCustomResource is the JSONPath to the field within the Custom Resource, e.g. 'spec.permission' or
'status.metadata.id'. For request entries it is the SOURCE of the value; for response entries it is
the CR-domain DESTINATION whose leaf name and parent path determine where the value lands.

Array elements can be addressed either by position ('spec.credentials[0].value') or, preferably, by
content with a predicate ('spec.credentials[?type=password].value'), which selects the single element
whose given field equals the given value. A predicate is shape-independent: it keeps targeting the
right element when the user lists the array in a different order, where a positional index would
silently address a different one. The predicate must match exactly one element — matching several is
an error rather than a silent first-wins, and on a request body it can only address an element that
already exists (it never invents one). Same syntax applies to inBody and to the resolver's
nameFromCustomResource/keyFromCustomResource.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inPath</b></td>
        <td>string</td>
        <td>
          InPath selects a REQUEST path parameter (request direction).
Only one of 'inPath', 'inQuery', 'inBody' or 'inResponse' can be set.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inQuery</b></td>
        <td>string</td>
        <td>
          InQuery selects a REQUEST query parameter (request direction).
Only one of 'inPath', 'inQuery', 'inBody' or 'inResponse' can be set.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inResponse</b></td>
        <td>string</td>
        <td>
          InResponse selects a RESPONSE body field by JSONPath (response direction).
The value found here is transformed (if valueMapping is set) and relocated to the CR-domain
destination given by inCustomResource, so that status population and drift comparison operate on the
CR-domain shape.
Only one of 'inPath', 'inQuery', 'inBody' or 'inResponse' can be set.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexresolver">resolver</a></b></td>
        <td>object</td>
        <td>
          Resolver, when set, sources the field's value from something other than a literal read of
inCustomResource — currently a secretRef, substituting a Kubernetes Secret's value. Applied
BEFORE valueMapping, so a resolved value may still be alias/jq-transformed afterward. Valid
only on request-direction entries (inPath/inQuery/inBody set).<br/>
          <br/>
            <i>Validations</i>:<li>self.type == 'secretRef' ? has(self.secretRef) : true: secretRef must be set when type is 'secretRef'</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexvaluemapping">valueMapping</a></b></td>
        <td>object</td>
        <td>
          ValueMapping optionally transforms the value as it crosses the CR<->API boundary.<br/>
          <br/>
            <i>Validations</i>:<li>self.type == 'alias' ? has(self.aliases) : true: aliases must be set when type is 'alias'</li><li>self.type == 'jq' ? has(self.jq) : true: jq must be set when type is 'jq'</li>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].fieldMapping[index].resolver
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexfieldmappingindex)</sup></sup>



Resolver, when set, sources the field's value from something other than a literal read of
inCustomResource — currently a secretRef, substituting a Kubernetes Secret's value. Applied
BEFORE valueMapping, so a resolved value may still be alias/jq-transformed afterward. Valid
only on request-direction entries (inPath/inQuery/inBody set).

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>type</b></td>
        <td>enum</td>
        <td>
          Type selects the resolver kind.<br/>
          <br/>
            <i>Enum</i>: secretRef<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexresolversecretref">secretRef</a></b></td>
        <td>object</td>
        <td>
          SecretRef substitutes a Kubernetes Secret's value for the field (used when type is 'secretRef'). The
Secret is always read from the Custom Resource instance's own namespace — there is no field to name a
different one.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].fieldMapping[index].resolver.secretRef
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexresolver)</sup></sup>



SecretRef substitutes a Kubernetes Secret's value for the field (used when type is 'secretRef'). The
Secret is always read from the Custom Resource instance's own namespace — there is no field to name a
different one.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>keyFromCustomResource</b></td>
        <td>string</td>
        <td>
          KeyFromCustomResource is a JSONPath into the Custom Resource yielding the key within the Secret's data.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>nameFromCustomResource</b></td>
        <td>string</td>
        <td>
          NameFromCustomResource is a JSONPath into the Custom Resource yielding the Secret's name.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].fieldMapping[index].valueMapping
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexfieldmappingindex)</sup></sup>



ValueMapping optionally transforms the value as it crosses the CR<->API boundary.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>type</b></td>
        <td>enum</td>
        <td>
          Type selects the transform tier.<br/>
          <br/>
            <i>Enum</i>: alias, jq<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexvaluemappingaliasesindex">aliases</a></b></td>
        <td>[]object</td>
        <td>
          Aliases is an explicit set of bidirectional CR<->API value pairs (used when type is 'alias').
On the request the CR value is rewritten to its apiValue; on the response the apiValue is rewritten
back to the CR value; any value without a matching pair passes through unchanged.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexvaluemappingjq">jq</a></b></td>
        <td>object</td>
        <td>
          JQ is a gojq program (used when type is 'jq'), supplied inline or as a referenced .jq module.
The program is one-directional: for a round-tripping field, write the inverse program in the
opposite-direction entry.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].fieldMapping[index].valueMapping.aliases[index]
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexvaluemapping)</sup></sup>



ValueAlias is a single bidirectional CR<->API value pair, e.g. {customResourceValue: read, apiValue: pull}.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>apiValue</b></td>
        <td>string</td>
        <td>
          APIValue is the corresponding value as expressed by the external API (API domain).<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>customResourceValue</b></td>
        <td>string</td>
        <td>
          CustomResourceValue is the value as expressed in the Custom Resource (CR domain).<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].fieldMapping[index].valueMapping.jq
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexvaluemapping)</sup></sup>



JQ is a gojq program (used when type is 'jq'), supplied inline or as a referenced .jq module.
The program is one-directional: for a round-tripping field, write the inverse program in the
opposite-direction entry.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].headers[index]
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindex)</sup></sup>



HeaderItem is a single static HTTP header injected on every request for a verb.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name is the HTTP header name, e.g. 'Accept' or 'Content-Type'.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>value</b></td>
        <td>string</td>
        <td>
          Value is the HTTP header value, sent verbatim.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].notFoundBody
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindex)</sup></sup>



NotFoundBody is the body-based counterpart of NotFoundCodes: a gojq predicate evaluated against the
successful (2xx) observe-response. When it yields true the external resource is treated as NOT
existing, so the reconciler creates it. Use it for APIs that signal absence with a 200 body rather
than a status code. The program must return a boolean, and its input '.' is the RAW body, whose shape
differs by verb:
  - get:    the whole GET body — e.g. `.items | length == 0`, `.deleted == true`, `.status == "NOT_FOUND"`.
  - findby: the SINGLE matched item (a findby no-match already yields not-found on its own), so write
            it against the item — e.g. a tombstone `.status == "deleted"`; a list-shaped predicate is
            meaningless here.
Intended for get/findby only. It is not evaluated while the resource is Pending (mid async create), and
a non-boolean result is a reconcile error.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].pagination
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindex)</sup></sup>



Pagination defines the pagination strategy for 'findby' actions. To be set only for 'findby' actions.
If not set, no pagination will be used.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>type</b></td>
        <td>enum</td>
        <td>
          Type specifies the pagination strategy.<br/>
          <br/>
            <i>Enum</i>: continuationToken, pageNumber<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexpaginationcontinuationtoken">continuationToken</a></b></td>
        <td>object</td>
        <td>
          Configuration for 'continuationToken' pagination. Required if type is 'continuationToken'.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexpaginationpagenumber">pageNumber</a></b></td>
        <td>object</td>
        <td>
          Configuration for 'pageNumber' pagination. Required if type is 'pageNumber'.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].pagination.continuationToken
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexpagination)</sup></sup>



Configuration for 'continuationToken' pagination. Required if type is 'continuationToken'.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexpaginationcontinuationtokenrequest">request</a></b></td>
        <td>object</td>
        <td>
          Request: defines how to include the pagination token in the API request.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexpaginationcontinuationtokenresponse">response</a></b></td>
        <td>object</td>
        <td>
          Response: defines how to extract the pagination token from the API response.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].pagination.continuationToken.request
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexpaginationcontinuationtoken)</sup></sup>



Request: defines how to include the pagination token in the API request.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>tokenIn</b></td>
        <td>enum</td>
        <td>
          Where the token is located: "query", "header" or "body". Currently, only "query" is supported.<br/>
          <br/>
            <i>Enum</i>: query<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>tokenPath</b></td>
        <td>string</td>
        <td>
          The path or name of the query parameter, header, or body field.
For query parameters and headers, this is simply the name.
For body fields, this should be a JSON path.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].pagination.continuationToken.response
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexpaginationcontinuationtoken)</sup></sup>



Response: defines how to extract the pagination token from the API response.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>tokenIn</b></td>
        <td>enum</td>
        <td>
          Where the token is located: "header" or "body".

"body" was listed here in prose while the enum admitted only "header", because the body branch of
the extractor was a `// Not implemented yet` comment that fell through to "no token" -- i.e. a
body token would have silently ended the walk after page one. It is implemented now, through the
same ResponseValue primitive the pageNumber strategy uses, so the enum admits it (#119).<br/>
          <br/>
            <i>Enum</i>: header, body<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>tokenPath</b></td>
        <td>string</td>
        <td>
          The path or name of the header or body field.
For headers, this is simply the name.
For body fields, this should be a JSON path.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].pagination.pageNumber
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexpagination)</sup></sup>



Configuration for 'pageNumber' pagination. Required if type is 'pageNumber'.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>maxPages</b></td>
        <td>integer</td>
        <td>
          MaxPages bounds the walk. REQUIRED, with no default, deliberately: the author must state how far
this search may go rather than inheriting a number they never considered.

Exhausting it is reported as its own outcome, NEVER as not-found. "I scanned N pages and did not
conclude" is not "it does not exist", and collapsing the two would recreate the very bug this
strategy exists to fix -- with the added insult that the bound was deliberate.<br/>
          <br/>
            <i>Minimum</i>: 1<br/>
            <i>Maximum</i>: 1000<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexpaginationpagenumberrequest">request</a></b></td>
        <td>object</td>
        <td>
          Request: how the page number (and optionally the page size) are sent.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexpaginationpagenumberresponse">response</a></b></td>
        <td>object</td>
        <td>
          Response: how "is there another page" is recognised. Optional: omitting it selects the
short-page rule, which is the common case.<br/>
          <br/>
            <i>Validations</i>:<li>!(has(self.header) && has(self.body)): declare at most one of header or body: two ways to recognise the last page cannot both be authoritative</li>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].pagination.pageNumber.request
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexpaginationpagenumber)</sup></sup>



Request: how the page number (and optionally the page size) are sent.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>pageIn</b></td>
        <td>enum</td>
        <td>
          Where the page number goes. Only "query" is supported: no API in practice paginates by header
page number, and allowing it would be untested surface.<br/>
          <br/>
            <i>Enum</i>: query<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>pagePath</b></td>
        <td>string</td>
        <td>
          PagePath is the parameter name carrying the page number, e.g. "page".<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>startPage</b></td>
        <td>integer</td>
        <td>
          StartPage is the number of the FIRST page. 1 for GitHub and most APIs; 0-based ones say 0.
Required rather than defaulted: a wrong guess here silently skips or repeats a page.<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>pageSize</b></td>
        <td>integer</td>
        <td>
          <br/>
          <br/>
            <i>Minimum</i>: 1<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>sizeIn</b></td>
        <td>enum</td>
        <td>
          SizeIn / SizePath / PageSize optionally request a page size. Omit them to accept the API's
default -- but note the short-page rule cannot work without PageSize, since it compares the
number of items returned against the number requested.<br/>
          <br/>
            <i>Enum</i>: query<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>sizePath</b></td>
        <td>string</td>
        <td>
          <br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].pagination.pageNumber.response
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexpaginationpagenumber)</sup></sup>



Response: how "is there another page" is recognised. Optional: omitting it selects the
short-page rule, which is the common case.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexpaginationpagenumberresponsebody">body</a></b></td>
        <td>object</td>
        <td>
          Body recognises the end by comparing against a total the API reports.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.totalPagesPath) != has(self.totalItemsPath): declare exactly one of totalPagesPath or totalItemsPath</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexpaginationpagenumberresponseheader">header</a></b></td>
        <td>object</td>
        <td>
          Header recognises "more pages exist" by matching a response header, e.g. Link containing
rel="next". Covers GitHub, GitLab and Jira without the engine knowing any of their names.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].pagination.pageNumber.response.body
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexpaginationpagenumberresponse)</sup></sup>



Body recognises the end by comparing against a total the API reports.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>totalItemsPath</b></td>
        <td>string</td>
        <td>
          TotalItemsPath is a path to the total NUMBER OF ITEMS, e.g. ".total_count". Requires
request.pageSize, since pages are derived from items per page.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>totalPagesPath</b></td>
        <td>string</td>
        <td>
          TotalPagesPath is a path to the total NUMBER OF PAGES, e.g. ".total_pages".<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].pagination.pageNumber.response.header
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexpaginationpagenumberresponse)</sup></sup>



Header recognises "more pages exist" by matching a response header, e.g. Link containing
rel="next". Covers GitHub, GitLab and Jira without the engine knowing any of their names.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>matches</b></td>
        <td>string</td>
        <td>
          Matches is a substring whose PRESENCE means another page exists, e.g. `rel="next"`.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of the header, e.g. "Link".<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].queries[index]
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindex)</sup></sup>



QueryParam is a single static query parameter injected on every request for a verb.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name is the query parameter name, e.g. 'api-version'.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>value</b></td>
        <td>string</td>
        <td>
          Value is the query parameter value, sent verbatim.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].requestFieldMapping[index]
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindex)</sup></sup>



RequestFieldMappingItem defines a single mapping from a path parameter, query parameter or body field
to a field in the Custom Resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>inCustomResource</b></td>
        <td>string</td>
        <td>
          InCustomResource defines the JSONPath to the field within the Custom Resource that holds the value.
For example: 'spec.name' or 'status.metadata.id'.
Note: potentially we could add validation to ensure this is a valid path (e.g., starts with 'spec.' or 'status.').
Currently, no validation is enforced on the content of this field.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>inBody</b></td>
        <td>string</td>
        <td>
          InBody defines the name of the body parameter to be mapped.
Only one of 'inPath', 'inQuery' or 'inBody' can be set.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inPath</b></td>
        <td>string</td>
        <td>
          InPath defines the name of the path parameter to be mapped.
Only one of 'inPath', 'inQuery' or 'inBody' can be set.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inQuery</b></td>
        <td>string</td>
        <td>
          InQuery defines the name of the query parameter to be mapped.
Only one of 'inPath', 'inQuery' or 'inBody' can be set.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].requestTransform
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindex)</sup></sup>



RequestTransform is a whole-document gojq program applied to the assembled request body immediately
before it is sent — the document-scoped sibling of a per-field jq valueMapping, mirroring
ResponseTransform. Input '.' is the entire body and the single output replaces it.

It runs AFTER the per-field fieldMapping entries have composed the body, so the program sees the
finished article. That is the inverse of ResponseTransform, which runs BEFORE per-field mapping —
both orderings exist so the document program always gets the API-shaped view.

Applies only where a body is sent (create/update/delete); on a bodyless verb it is a no-op rather
than an invented body. A program that fails to compile or run fails the reconcile rather than
sending a partially transformed body.

Requires rest-dynamic-controller >= 0.17.0. Between oasgen 0.16.0 and this release the field was
REJECTED at admission: it was accepted by the schema and executed by nothing, and rejecting it was
preferable to letting it be a silent no-op.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].responseTransform
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindex)</sup></sup>



ResponseTransform is a whole-document gojq program applied to the raw response body once, at the
reconcile chokepoint, before per-field fieldMapping, status population and drift comparison. Input
'.' is the entire response body and the single output replaces it. It is the declarative form of a
plugin's whole-body response normalizer.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.configurationFields[index]
<sup><sup>[↩ Parent](#restdefinitionspecresource)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#restdefinitionspecresourceconfigurationfieldsindexfromopenapi">fromOpenAPI</a></b></td>
        <td>object</td>
        <td>
          <br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceconfigurationfieldsindexfromrestdefinition">fromRestDefinition</a></b></td>
        <td>object</td>
        <td>
          <br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.configurationFields[index].fromOpenAPI
<sup><sup>[↩ Parent](#restdefinitionspecresourceconfigurationfieldsindex)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>in</b></td>
        <td>string</td>
        <td>
          <br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          <br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.configurationFields[index].fromRestDefinition
<sup><sup>[↩ Parent](#restdefinitionspecresourceconfigurationfieldsindex)</sup></sup>





<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>actions</b></td>
        <td>[]string</td>
        <td>
          Actions: the list of actions this configuration applies to. Use ["*"] to apply to all actions.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.createApiRef
<sup><sup>[↩ Parent](#restdefinitionspecresource)</sup></sup>



CreateApiRef, when set, delegates CREATE of this resource to a Snowplow RESTAction instead of the
create verb: the controller invokes the referenced RESTAction (passing the resource's name/namespace/
uid and its whole spec — the desired state — as request extras) to run the multi-call provisioning
sequence, and projects any composed .status it returns into this resource's status. The RESTAction
MUST be idempotent: the controller does not verify per-call success — it re-invokes create every
reconcile until Observe reports the resource exists (level-based convergence). This therefore REQUIRES
a get/findby verb — OR an observeApiRef whose notFoundExpr reports non-existence — the observe that
reports non-existence; with none, the resource is marked Available after a single unverified
invocation. Dissolves proxies whose only job is to chain create calls (e.g. create instance -> attach
disk -> start).

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of the RESTAction to resolve.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>namespace</b></td>
        <td>string</td>
        <td>
          Namespace of the RESTAction.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>extras</b></td>
        <td>JSON</td>
        <td>
          Extras are static key/values merged UNDER the per-instance context (this resource's name, namespace,
uid and spec) that the controller passes to snowplow as request extras; the per-instance context wins
on conflict. Use them to parameterize the RESTAction (e.g. a fixed endpoint or api-version).<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourcecreateapirefnotfoundexpr">notFoundExpr</a></b></td>
        <td>object</td>
        <td>
          NotFoundExpr (observeApiRef only) is a gojq boolean predicate evaluated against {spec, status}, where
status is the RESTAction's composed result. When it returns true the resource is reported as NOT
existing, so the controller creates it — this is what lets observeApiRef compose with createApiRef
(which otherwise it cannot, because a delegated observe reports existence unconditionally).<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourcecreateapirefuptodateexpr">upToDateExpr</a></b></td>
        <td>object</td>
        <td>
          UpToDateExpr (observeApiRef only) is a gojq boolean predicate over {spec, status}. When it returns
false the resource is reported as drifted, so the controller updates it (composing with updateApiRef).
Absent => the resource is assumed up-to-date.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.createApiRef.notFoundExpr
<sup><sup>[↩ Parent](#restdefinitionspecresourcecreateapiref)</sup></sup>



NotFoundExpr (observeApiRef only) is a gojq boolean predicate evaluated against {spec, status}, where
status is the RESTAction's composed result. When it returns true the resource is reported as NOT
existing, so the controller creates it — this is what lets observeApiRef compose with createApiRef
(which otherwise it cannot, because a delegated observe reports existence unconditionally).

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.createApiRef.upToDateExpr
<sup><sup>[↩ Parent](#restdefinitionspecresourcecreateapiref)</sup></sup>



UpToDateExpr (observeApiRef only) is a gojq boolean predicate over {spec, status}. When it returns
false the resource is reported as drifted, so the controller updates it (composing with updateApiRef).
Absent => the resource is assumed up-to-date.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.deleteApiRef
<sup><sup>[↩ Parent](#restdefinitionspecresource)</sup></sup>



DeleteApiRef, when set, delegates DELETE of this resource to a Snowplow RESTAction instead of the
delete verb: on deletion the controller invokes the referenced RESTAction (the teardown sequence) and
holds the finalizer until it succeeds. The RESTAction MUST be idempotent and tolerate an already-gone
sub-resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of the RESTAction to resolve.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>namespace</b></td>
        <td>string</td>
        <td>
          Namespace of the RESTAction.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>extras</b></td>
        <td>JSON</td>
        <td>
          Extras are static key/values merged UNDER the per-instance context (this resource's name, namespace,
uid and spec) that the controller passes to snowplow as request extras; the per-instance context wins
on conflict. Use them to parameterize the RESTAction (e.g. a fixed endpoint or api-version).<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourcedeleteapirefnotfoundexpr">notFoundExpr</a></b></td>
        <td>object</td>
        <td>
          NotFoundExpr (observeApiRef only) is a gojq boolean predicate evaluated against {spec, status}, where
status is the RESTAction's composed result. When it returns true the resource is reported as NOT
existing, so the controller creates it — this is what lets observeApiRef compose with createApiRef
(which otherwise it cannot, because a delegated observe reports existence unconditionally).<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourcedeleteapirefuptodateexpr">upToDateExpr</a></b></td>
        <td>object</td>
        <td>
          UpToDateExpr (observeApiRef only) is a gojq boolean predicate over {spec, status}. When it returns
false the resource is reported as drifted, so the controller updates it (composing with updateApiRef).
Absent => the resource is assumed up-to-date.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.deleteApiRef.notFoundExpr
<sup><sup>[↩ Parent](#restdefinitionspecresourcedeleteapiref)</sup></sup>



NotFoundExpr (observeApiRef only) is a gojq boolean predicate evaluated against {spec, status}, where
status is the RESTAction's composed result. When it returns true the resource is reported as NOT
existing, so the controller creates it — this is what lets observeApiRef compose with createApiRef
(which otherwise it cannot, because a delegated observe reports existence unconditionally).

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.deleteApiRef.upToDateExpr
<sup><sup>[↩ Parent](#restdefinitionspecresourcedeleteapiref)</sup></sup>



UpToDateExpr (observeApiRef only) is a gojq boolean predicate over {spec, status}. When it returns
false the resource is reported as drifted, so the controller updates it (composing with updateApiRef).
Absent => the resource is assumed up-to-date.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.observeApiRef
<sup><sup>[↩ Parent](#restdefinitionspecresource)</sup></sup>



ObserveApiRef, when set, delegates the OBSERVE of this resource to a Snowplow RESTAction instead of the
get/findby verbs: the rest-dynamic-controller invokes the referenced RESTAction (via snowplow /call,
under its own identity) each reconcile — passing the resource's name/namespace/uid and its identifier
values (not the whole spec) as request extras — and projects the RESTAction's composed .status into
this resource's status (leaving the runtime-managed conditions untouched). It dissolves proxies whose
only job is to compose a multi-call observation (read several sub-resources and shape one status). The
referenced RESTAction is trusted platform configuration. Being reconcile behavior rather than CRD
shape, it is intentionally mutable.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of the RESTAction to resolve.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>namespace</b></td>
        <td>string</td>
        <td>
          Namespace of the RESTAction.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>extras</b></td>
        <td>JSON</td>
        <td>
          Extras are static key/values merged UNDER the per-instance context (this resource's name, namespace,
uid and spec) that the controller passes to snowplow as request extras; the per-instance context wins
on conflict. Use them to parameterize the RESTAction (e.g. a fixed endpoint or api-version).<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceobserveapirefnotfoundexpr">notFoundExpr</a></b></td>
        <td>object</td>
        <td>
          NotFoundExpr (observeApiRef only) is a gojq boolean predicate evaluated against {spec, status}, where
status is the RESTAction's composed result. When it returns true the resource is reported as NOT
existing, so the controller creates it — this is what lets observeApiRef compose with createApiRef
(which otherwise it cannot, because a delegated observe reports existence unconditionally).<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceobserveapirefuptodateexpr">upToDateExpr</a></b></td>
        <td>object</td>
        <td>
          UpToDateExpr (observeApiRef only) is a gojq boolean predicate over {spec, status}. When it returns
false the resource is reported as drifted, so the controller updates it (composing with updateApiRef).
Absent => the resource is assumed up-to-date.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.observeApiRef.notFoundExpr
<sup><sup>[↩ Parent](#restdefinitionspecresourceobserveapiref)</sup></sup>



NotFoundExpr (observeApiRef only) is a gojq boolean predicate evaluated against {spec, status}, where
status is the RESTAction's composed result. When it returns true the resource is reported as NOT
existing, so the controller creates it — this is what lets observeApiRef compose with createApiRef
(which otherwise it cannot, because a delegated observe reports existence unconditionally).

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.observeApiRef.upToDateExpr
<sup><sup>[↩ Parent](#restdefinitionspecresourceobserveapiref)</sup></sup>



UpToDateExpr (observeApiRef only) is a gojq boolean predicate over {spec, status}. When it returns
false the resource is reported as drifted, so the controller updates it (composing with updateApiRef).
Absent => the resource is assumed up-to-date.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.updateApiRef
<sup><sup>[↩ Parent](#restdefinitionspecresource)</sup></sup>



UpdateApiRef, when set, delegates UPDATE of this resource to a Snowplow RESTAction instead of the
update verb: when Observe reports drift the controller invokes the referenced RESTAction (passing the
whole spec — the desired state) to re-apply it. Like create, the RESTAction MUST be idempotent.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name of the RESTAction to resolve.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>namespace</b></td>
        <td>string</td>
        <td>
          Namespace of the RESTAction.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>extras</b></td>
        <td>JSON</td>
        <td>
          Extras are static key/values merged UNDER the per-instance context (this resource's name, namespace,
uid and spec) that the controller passes to snowplow as request extras; the per-instance context wins
on conflict. Use them to parameterize the RESTAction (e.g. a fixed endpoint or api-version).<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceupdateapirefnotfoundexpr">notFoundExpr</a></b></td>
        <td>object</td>
        <td>
          NotFoundExpr (observeApiRef only) is a gojq boolean predicate evaluated against {spec, status}, where
status is the RESTAction's composed result. When it returns true the resource is reported as NOT
existing, so the controller creates it — this is what lets observeApiRef compose with createApiRef
(which otherwise it cannot, because a delegated observe reports existence unconditionally).<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceupdateapirefuptodateexpr">upToDateExpr</a></b></td>
        <td>object</td>
        <td>
          UpToDateExpr (observeApiRef only) is a gojq boolean predicate over {spec, status}. When it returns
false the resource is reported as drifted, so the controller updates it (composing with updateApiRef).
Absent => the resource is assumed up-to-date.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.updateApiRef.notFoundExpr
<sup><sup>[↩ Parent](#restdefinitionspecresourceupdateapiref)</sup></sup>



NotFoundExpr (observeApiRef only) is a gojq boolean predicate evaluated against {spec, status}, where
status is the RESTAction's composed result. When it returns true the resource is reported as NOT
existing, so the controller creates it — this is what lets observeApiRef compose with createApiRef
(which otherwise it cannot, because a delegated observe reports existence unconditionally).

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.updateApiRef.upToDateExpr
<sup><sup>[↩ Parent](#restdefinitionspecresourceupdateapiref)</sup></sup>



UpToDateExpr (observeApiRef only) is a gojq boolean predicate over {spec, status}. When it returns
false the resource is reported as drifted, so the controller updates it (composing with updateApiRef).
Absent => the resource is assumed up-to-date.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>entrypoint</b></td>
        <td>string</td>
        <td>
          Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
If empty, the whole module body is executed as the program. Only meaningful together with ref.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inline</b></td>
        <td>string</td>
        <td>
          Inline is a gojq source literal. Best for short, single-use expressions.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ref</b></td>
        <td>string</td>
        <td>
          Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
  configmap://<namespace>/<name>/<key>   |   http(s)://<url><br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.status
<sup><sup>[↩ Parent](#restdefinition)</sup></sup>



RestDefinitionStatus is the status of a RestDefinition.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>oasPath</b></td>
        <td>string</td>
        <td>
          OASPath: the path to the OAS Specification file.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>authSecretDigest</b></td>
        <td>string</td>
        <td>
          AuthSecretDigest: a hash of the Secret names (grouped by namespace) currently referenced, across every
Configuration CR instance of this RestDefinition's Configuration Kind, by usernameRef/passwordRef/
tokenRef. Observe re-collects and re-hashes these references each reconcile and treats a change as
drift, so RDC's per-namespace auth-secret RBAC (see AuthSecretRBACNamespaces) is refreshed whenever a
Configuration instance starts, stops, or changes which Secret it authenticates with.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>authSecretRBACNamespaces</b></td>
        <td>[]string</td>
        <td>
          AuthSecretRBACNamespaces: the namespaces in which a namespace-scoped Role+RoleBinding currently grants
RDC's ServiceAccount read access to the auth Secrets collected above. Tracked so a namespace that no
longer has any referencing Configuration instance has its now-unneeded RBAC removed, and so RestDefinition
delete can tear down every namespace's RBAC without re-listing (possibly already-gone) Configuration instances.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionstatusconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          Conditions of the resource.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionstatusconfiguration">configuration</a></b></td>
        <td>object</td>
        <td>
          Configuration: the configuration of the resource<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>digest</b></td>
        <td>string</td>
        <td>
          Digest: the digest of the managed resources<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>hasSecuritySchemes</b></td>
        <td>boolean</td>
        <td>
          HasSecuritySchemes: whether the OAS document defines security schemes.
Cached here so Observe does not need to re-fetch the OAS document on every reconcile.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>oasHash</b></td>
        <td>string</td>
        <td>
          OASHash: a content hash of the resolved OAS document at the last successful Create/Update. Observe
re-hashes the referenced OAS each reconcile and treats a change as drift, so an edit to the OAS source
(e.g. the referenced ConfigMap) is picked up even when oasPath itself is unchanged.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionstatusresource">resource</a></b></td>
        <td>object</td>
        <td>
          Resource: the resource to manage<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.status.conditions[index]
<sup><sup>[↩ Parent](#restdefinitionstatus)</sup></sup>



A Condition that may apply to a resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>lastTransitionTime</b></td>
        <td>string</td>
        <td>
          LastTransitionTime is the last time this condition transitioned from one
status to another.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>reason</b></td>
        <td>string</td>
        <td>
          A Reason for this condition's last transition from one status to another.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>status</b></td>
        <td>string</td>
        <td>
          Status of this condition; is it currently True, False, or Unknown?<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>string</td>
        <td>
          Type of this condition. At most one of each condition type may apply to
a resource at any point in time.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>message</b></td>
        <td>string</td>
        <td>
          A Message containing details about this condition's last transition from
one status to another, if any.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.status.configuration
<sup><sup>[↩ Parent](#restdefinitionstatus)</sup></sup>



Configuration: the configuration of the resource

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>apiVersion</b></td>
        <td>string</td>
        <td>
          APIVersion: the api version of the resource<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>kind</b></td>
        <td>string</td>
        <td>
          Kind: the kind of the resource<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.status.resource
<sup><sup>[↩ Parent](#restdefinitionstatus)</sup></sup>



Resource: the resource to manage

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>apiVersion</b></td>
        <td>string</td>
        <td>
          APIVersion: the api version of the resource<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>kind</b></td>
        <td>string</td>
        <td>
          Kind: the kind of the resource<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>
