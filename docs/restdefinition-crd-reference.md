---
type: CRD
title: RestDefinition CRD reference
description: Generated field-by-field reference for restdefinitions.ogen.krateo.io (crdoc output from go/oasgen-provider/crds; regenerate after `make generate`).
resource: restdefinitions.ogen.krateo.io
tags: [kog, crd, restdefinition, generated]
timestamp: 2026-10-20T00:00:00Z
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
            <i>Validations</i>:<li>!(has(self.createApiRef) && has(self.observeApiRef) && !has(self.observeApiRef.notFoundExpr)): createApiRef with observeApiRef requires observeApiRef.notFoundExpr, so a create can be triggered when the delegated observe reports the resource absent</li><li>!has(self.createApiRef) || self.verbsDescription.exists(v, v.action == 'get' || v.action == 'findby'): createApiRef requires a get or findby verb so the controller can verify the create converged (level-based convergence)</li><li>!has(self.compareScope) || self.compareScope != 'identifiersAndStatus' || (has(self.identifiers) && size(self.identifiers) > 0) || (has(self.additionalStatusFields) && size(self.additionalStatusFields) > 0): compareScope 'identifiersAndStatus' requires at least one identifier or additionalStatusField to compare against</li><li>!has(self.compareScope) || self.compareScope != 'updatable' || self.verbsDescription.exists(v, v.action == 'update'): compareScope 'updatable' requires an update verb: with no update verb the set of updatable fields is empty, so nothing would ever be compared and the resource would silently never report drift</li><li>!(has(self.includedSpecFields) && has(self.excludedSpecFields)): declare at most one of includedSpecFields or excludedSpecFields: an allowlist and a blocklist over the same fields is a contradiction, not a merge</li><li>!self.verbsDescription.exists(v, v.action == 'dispatch') || !self.verbsDescription.exists(v, v.action == 'create' || v.action == 'update'): a 'dispatch' verb cannot be combined with 'create' or 'update': the dispatch IS the create, and there is nothing an update of a past invocation could mean</li><li>self.verbsDescription.filter(v, v.action == 'dispatch').size() <= 1: declare at most one 'dispatch' verb: two dispatches on one kind have no defined order and no way to tell which one a correlated invocation came from</li><li>!self.verbsDescription.exists(v, v.action == 'dispatch') || !(has(self.createApiRef) || has(self.updateApiRef) || has(self.deleteApiRef) || has(self.observeApiRef)): a 'dispatch' verb cannot be combined with any *ApiRef: delegating to a Snowplow RESTAction and declaring a correlated dispatch are the two alternative answers to the same problem, and declaring both leaves which one runs decided by the order of checks in the controller rather than by the author</li><li>!has(self.dependentLookups) || self.dependentLookups.all(i, l, !has(l.parameters) || l.parameters.all(p, !has(p.fromLookup) || self.dependentLookups.exists(j, o, j < i && o.name == p.fromLookup))): a dependentLookups parameter's fromLookup must name a lookup declared EARLIER in the list: resolution is in declaration order, and restricting references to preceding entries makes a cycle unrepresentable rather than something detected at runtime</li><li>!self.verbsDescription.exists(v, v.action == 'dispatch') || !has(self.compareScope): compareScope cannot be set alongside a 'dispatch' verb: a dispatch is compared on its correlation token alone, so any other scope would make an unrelated spec edit read as drift on a resource whose only response to drift is to fire the dispatch again</li>
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
      </tr><tr>
        <td><b>serverURL</b></td>
        <td>string</td>
        <td>
          ServerURL overrides the base URL the document declares in `servers[0]`, for every request this
resource makes. It exists for a self-hosted instance of a service whose published OpenAPI document
names the vendor's own endpoint: GitHub Enterprise Server serves the same API as github.com, at
https://<host>/api/v3.

It REPLACES servers[0] rather than adding to it, and that is a security property rather than a
convenience. The endpoint host policy is built from the document's servers, so on a self-hosted
install the vendor's public host stops being permitted at all -- a misconfigured install fails
closed instead of reaching the public service with the credential meant for the private one.

Give it a WHOLE base URL, scheme included, and keep the path:
  https://ghe.corp.example.com/api/v3      correct
  https://ghe.corp.example.com:8443/api/v3 correct, port preserved
  ghe.corp.example.com                     REJECTED, no scheme
  https://ghe.corp.example.com             accepted, but on GHES every request 404s -- the
                                           /api/v3 prefix is part of the base URL, not the paths

A trailing slash is tolerated. The field is deliberately NOT a host: a name containing "host"
invites exactly the prefix-less value on the last line, which is accepted here and fails much
later, at the vendor.

It is MUTABLE, unlike resourceGroup. Correcting a self-hosted hostname is a legitimate edit, and it
takes effect on the next reconcile with no restart, because the RestDefinition is read before the
client is constructed at every entry point. It also sits OUTSIDE spec.resource on purpose:
resourceHash marshals spec.resource only and that hash gates CRD regeneration, so a field inside it
would re-apply the resource CRD on every hostname change, for a value the generated schema does not
depend on.<br/>
        </td>
        <td>false</td>
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
          VerbsDescription: the list of verbs to use on this resource

A per-verb oasPath override used to be refused here, because the field shipped in the CRD and
NOTHING READ IT: the write was admitted and the verb resolved from spec.oasPath regardless, silently
generating the CRD from the wrong document. The rule is gone because #108 implemented it -- every
per-verb lookup now resolves against the verb's own document, and two documents that disagree about
a merged parameter are refused at generation rather than resolved by verb order.

The MaxItems=32 bound went with it. It was never a limit on verbs; it existed only to keep that
rule's comprehension inside the apiserver's CEL cost budget, which an unbounded one exceeds at
CRD-install time -- taking the whole CRD down rather than just the validation (#169).<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>additionalStatusFields</b></td>
        <td>[]string</td>
        <td>
          AdditionalStatusFields: the list of fields to use as additional status fields - used to populate the status of the resource

APPEND-ONLY, not immutable. Entries may be added at the end; the existing ones must stay in place,
in order, unchanged.

Fully immutable was too strong and blocked an ordinary upgrade: a KOG chart that adds a status field
-- github-provider-kog 0.3.2 adding `merged` and `merged_at` to PullRequest -- could not be applied
over an existing install at all, and the only way through was deleting the RestDefinition and its
CRs. On the cluster that hit it those CRs were 24 publish records, so that was not an option.

Appending is safe: the generated CRD gains optional status properties (and any printer columns), and
stored objects simply do not carry them yet. The operations still refused are the ones that break
stored objects or columns -- removing, renaming, reordering, or changing an existing entry.

The rule is a prefix comparison. `oldSelf.all(i, v, self[i] == v)` is a TWO-VARIABLE comprehension,
available in the apiserver's CEL from Kubernetes 1.32, which is below this chart's 1.36 floor. The
obvious alternative, `self.slice(0, size(oldSelf)) == oldSelf`, needs ext.Lists, which the apiserver
only enables from 1.34 -- and a CRD whose CEL does not compile is rejected outright, so that would
have broken installs on 1.33 rather than merely failing to validate.

MaxItems and MaxLength are REQUIRED for the rule above, not stylistic. Kubernetes costs a CEL rule
statically, and a comprehension over an unbounded list of unbounded strings estimates as effectively
infinite: the apiserver refused the whole CRD with "estimated rule cost exceeds budget by factor of
more than 100x", which rejects the install rather than the RestDefinition. The bounds make the
estimate finite. They are generous -- a resource with more than 64 additional status fields, or a
field path longer than 253 characters, is not a thing we have seen.<br/>
          <br/>
            <i>Validations</i>:<li>size(self) >= size(oldSelf) && oldSelf.all(i, v, self[i] == v): AdditionalStatusFields is append-only: you may add new entries at the end, but existing entries cannot be removed, renamed, reordered or changed once the CRD has been generated</li>
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
          ConfigurationFields: the list of fields to use as configuration fields
MUTABLE since this change, on the principle #666 settled: gate on DATA LOSS, not on symmetry.

	ADD     a spec property is pruned; whatever live CRs hold there goes with it, irreversibly
	        -> DESTRUCTIVE, and warned about by name before the apply
	REMOVE  the property comes back -> ADDITIVE; nothing is ever lost -> no gate

This field had the SAME shape as excludedSpecFields and was named in #666's own symptom list with
the identical error message -- that issue documented two frozen fields and unfroze one. Adding an
entry calls removeConfiguredFields from the SPEC builder, so the property is pruned exactly as an
exclusion prunes it; removing one lets it return.

NOT append-only, for the same reason excludedSpecFields is not: removals lose nothing, so there is
nothing for a gate to protect. And it is the REMOVAL that a consumer needs in the case that
motivated #666 -- a credential sitting in a CR spec, where the remedy is to move it into the
Configuration CR and the author has to be able to change their mind afterwards.

WHY identifiers IS STILL IMMUTABLE and this is not, since they sit two fields apart: both of that
field's directions lose something. Adding an identifier under `AND` makes matching STRICTER, so a
CR that matched its external object can stop matching and be orphaned or re-created; removing one
makes it LOOSER, which is #444's measured case -- a CR adopted a different object on a shared
non-unique field, reported Synced=True, and then deleted it. The principle keeps that field frozen
and frees this one; it is not an inconsistency.<br/>
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
        <td><b><a href="#restdefinitionspecresourcedependentlookupsindex">dependentLookups</a></b></td>
        <td>[]object</td>
        <td>
          DependentLookups declares named, pre-call reads whose selected value feeds a field of the main
request, via a fieldMapping entry whose resolver names the lookup.

This is the shape apiLookup could not express and was removed for (ff66424): its Action had to name
a verb in verbsDescription, and that field is a closed enum, so an auxiliary endpoint could not be
declared at all. A lookup is declared HERE, as a sibling of verbsDescription, precisely so it can
name any endpoint in the document without widening that enum.

Two consumers define this shape, filed independently by two providers:
  - oasgen-provider#108's GitHub GitRef needs a CHAIN -- the default branch, then that branch's sha
    -- where the second read's path parameter is the first read's result.
  - rest-dynamic-controller#30's KeycloakClient needs client-side MATCHING over a list: find the
    authentication flow whose alias equals a CR field, take its id.

MaxItems is REQUIRED, not stylistic: the uniqueness and ordering rules below are CEL comprehensions,
and Kubernetes costs a rule statically against the declared bound. An unbounded list estimates as
effectively infinite and the apiserver refuses the whole CRD -- taking the install down rather than
the RestDefinition (#169). Eight is generous; the two known consumers use two and one.<br/>
          <br/>
            <i>Validations</i>:<li>self.all(l, self.filter(o, o.name == l.name).size() == 1): dependentLookups names must be unique: a fieldMapping resolver names a lookup, so two lookups sharing a name leave which one runs decided by list order rather than by the author</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>excludedSpecFields</b></td>
        <td>[]string</td>
        <td>
          ExcludedSpecFields: the list of fields to exclude from the spec of the generated CRD (for example
server-generated technical IDs could be excluded)

MUTABLE, and deliberately not append-only (#666). It was strictly immutable until 0.72.0, which made a
blocklist unrecoverable: a wide vendor schema inverts badly -- Keycloak's RealmRepresentation has 152
fields, so "I want 30 of them" means naming ~122 exclusions, and one wrong entry could only be undone by
deleting the RestDefinition and with it every CR of that kind. Append-only would not have helped, because
the recovery an author needs is REMOVING an entry they should never have added.

Changing it regenerates the CRD rather than being silently accepted: excludedSpecFields lives in
spec.resource, and status.resourceHash is a sha256 over the WHOLE of spec.resource, so an edit here is
drift on the next Observe and Update re-renders. That is the property that makes this safe to unfreeze,
and it is the one to re-check before unfreezing anything else in this struct.

REMOVING an entry widens the spec, which is always safe. ADDING one prunes a property the CRD may
already be serving, and any CR that still sets it will be rejected on its next apply -- so generation
emits a warning naming the field (status.schemaWarnings plus a Warning event) rather than refusing. The
apiserver prunes the field from stored CRs without consulting this provider, so refusing the edit would
not protect anything that is not already at risk; the author is the only one who can say whether the
field is still wanted.<br/>
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
        <td><b>includedSpecFields</b></td>
        <td>[]string</td>
        <td>
          IncludedSpecFields: the ALLOWLIST counterpart of excludedSpecFields. When set, the generated spec keeps
ONLY these fields (plus the machinery described below); when unset, nothing changes.

A blocklist inverts badly on a wide vendor schema. Keycloak's RealmRepresentation has 152 fields, so
"I want 30 of them" means naming ~122 exclusions, and a list that long is easy to get wrong. (Until
0.72.0 excludedSpecFields was also immutable, so getting it wrong once was unrecoverable without deleting
the RestDefinition and its CRs; #666 lifted that, but the INVERSION is the reason this field exists and
it is unaffected -- naming 122 exclusions correctly is still worse than naming 30 inclusions.) The Keycloak KOG
abandoned the vendor document over exactly this and hand-writes a curated OAS per kind -- a 328-line
generator plus a vendored subset standing in for a 30-line RestDefinition.

It is a correctness control as much as an economy. An unbounded generated schema is a liability at the
apiserver: a CRD has already been taken down here by the CEL cost budget (#169, "estimated rule cost
exceeds budget" is a CRD-INSTALL failure, not a validation failure). Bounding what gets generated is
load-bearing even for a single integration.

APPEND-ONLY rather than immutable, following additionalStatusFields. Widening a subset is safe -- the
CRD gains fields. Narrowing it removes fields that live CRs may be setting, which is the thing
immutability actually protects against. See the note on AdditionalStatusFields above for why the rule
is a prefix comparison and why the MaxItems/MaxLength bounds are required rather than stylistic: a CEL
comprehension over an unbounded list costs as effectively infinite and gets the whole CRD rejected.

Dot-notation selects nested fields, as in excludedSpecFields. A path naming a parent keeps that parent's
whole subtree; naming a child keeps the parent and only the listed children.

ALWAYS KEPT, whether listed or not, because they are machinery the resource cannot function without:
configurationRef (without it the resource has no way to reference its credentials), spec fields derived
from REQUIRED OAS parameters (without them the resource cannot build its own URL -- the same breakage
#197 fixed), and the identifier selectors materialised into the spec of a resource with no create verb
(without them findby can never match). Optional parameter-derived fields are ordinary fields and can be
dropped.

A listed field that does not exist in the generated schema is REFUSED, where excludedSpecFields only
warns. The asymmetry is deliberate: a typo in a blocklist silently leaves a field IN, where it is
visible and discoverable; a typo in an allowlist silently leaves a field OUT, and the author sees a
missing field with no explanation and a CR that fails validation on a field they believed they had
included.<br/>
          <br/>
            <i>Validations</i>:<li>size(self) >= size(oldSelf) && oldSelf.all(i, v, self[i] == v): IncludedSpecFields is append-only: you may add new entries at the end, but existing entries cannot be removed, renamed, reordered or changed once the CRD has been generated</li>
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
          Name of the action to perform when this api is called [create, update, get, delete, findby, dispatch]<br/>
          <br/>
            <i>Enum</i>: create, update, get, delete, findby, dispatch<br/>
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
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelation">correlation</a></b></td>
        <td>object</td>
        <td>
          Correlation is REQUIRED for, and permitted only on, a 'dispatch' action. See the Correlation type.<br/>
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
        <td><b>itemsPath</b></td>
        <td>string</td>
        <td>
          ItemsPath names the property holding the collection, for a 'findby' whose response is an ENVELOPE
rather than a bare array -- e.g. ".values" for {"total": 12, "values": [...]}. To be set only for
'findby' actions.

It resolves ONE question that is asked in two places and must get the same answer in both: the
generator reads it to find the schema of an item (so identifiers get their real types instead of
degrading to string), and the controller reads it at runtime to find the items themselves. When
those two disagree, the generated CRD describes a shape the controller never looks at.

Optional, because the overwhelmingly common envelope has exactly one array property and that one is
unambiguous. Declare it when there is more than one: an envelope like {"data": [...],
"included": [...]} (plain JSON:API) has no single obvious collection, and rather than pick one,
both sides refuse -- generation fails with the candidates named. Guessing here is not a smaller
version of being right; it silently searches the wrong list and reports not-found, which the
reconciler acts on by creating a duplicate.

Uses the same path dialect as async.poll.statusPath, fieldMapping and pagination body paths.<br/>
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
generated CRD traces back to one vendor document, which this repo checksum-enforces.

Where two documents disagree, the RestDefinition is REFUSED rather than silently preferring one --
at GENERATION, not admission, because admission cannot see the documents. The check is scoped to
the one place two documents demonstrably feed the same CRD field: parameters are merged across
every verb and deduplicated by name, so the same name declared differently by verbs reading
different documents would otherwise be resolved by the ORDER of this list.

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
ABSENT MEANS A SINGLE PAGE, AND THAT IS A CORRECTNESS SETTING RATHER THAN A SCALE ONE. The same
statement is on correlation.observe.pagination and it was missing here, which is the asymmetry that
cost a consumer an investigation: "no pagination will be used" reads as a performance note.

What it actually means: findby issues ONE request and matches within that response
(restclient.go refuses a nil paginator and calls CallFindBySingle). A resource that exists but falls
beyond the vendor's default page is reported as NOT FOUND -- and on a findby that is worse than the
dispatch case, because the listing succeeded. "Listed, nothing matched" is a SYNTHESISED not-found,
the create then stamps krateo.io/external-create-pending, and recovery asks the same findby, which
answers the same way. The CR latches at CreateOutcomeUnknown and needs a human (issue 818).

MEASURED: a realm with 105 users, where the vendor's default page is 100. A managed user sorting
after the page boundary was created at the vendor exactly once, reported absent, and stuck;
the control sorting first was Ready. No duplicate -- the resource exists and the CR cannot see it.

DECLARING PAGINATION IS NOT USUALLY THE RIGHT FIX. Narrowing the listing is: if the operation
accepts a query parameter that identifies the resource, declare it and the response is 0 or 1 items
regardless of collection size. Walking 100 items to find one is a worse answer than asking for one,
and it stays correct when the collection reaches 10,000. Pagination is for collections that cannot
be narrowed.

AND A NARROWING PARAMETER MUST BE EXACT. A substring filter returns several items, which issue 444
made a REFUSAL rather than an adoption of the first -- so a findby narrowed by a prefix match works
until two resources share that prefix, and then stops working. Where the vendor offers an
exact-match flag, send it as a constant through the Configuration CR's configurationFields rather
than as a spec field: configurationFields wins over a spec field of the same name, so the behaviour
cannot be switched off per resource.<br/>
          <br/>
            <i>Validations</i>:<li>self.type == 'continuationToken' ? has(self.continuationToken) : true: continuationToken configuration must be provided when type is 'continuationToken'</li><li>self.type == 'pageNumber' ? has(self.pageNumber) : true: pageNumber configuration must be provided when type is 'pageNumber'</li><li>self.type == 'continuationToken' ? !has(self.pageNumber) : true: pageNumber must not be set when type is 'continuationToken'</li><li>self.type == 'pageNumber' ? !has(self.continuationToken) : true: continuationToken must not be set when type is 'pageNumber'</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>partialFields</b></td>
        <td>[]string</td>
        <td>
          PartialFields names top-level spec fields this verb's body carries as a PARTIAL VIEW of what the
vendor holds there, rather than as the complete desired state.

WITHOUT IT, A PARTIAL VIEW IS ENFORCED AS THE WHOLE OBJECT. The request body is assembled by
top-level spec key, so the whole value goes out under its key and whatever the CR does not express
is deleted. Measured on two vendors, with controls:

  - Ironic: a CR modelling 2 of the 4 keys in node.extra deleted the other two. 82 seconds without
    a control-plane VIP, across 6 PATCHes. Blades whose spec.extra was ABSENT were sent none and
    kept theirs, which is what made it attributable.
  - GitHub: a webhook PATCH whose config omitted `secret` CLEARED the HMAC secret; a PATCH of
    `active` only kept it. Availability in the first case; INTEGRITY, silently, in the second --
    there is no observable difference between a hook that verifies signatures and one that does not.

WHAT IT DOES. Before the request, the controller reads the resource and overlays the CR's keys onto
the vendor's current value for each named field. The CR wins on every key it expresses; every other
key the vendor holds survives.

IF THAT READ FAILS, THE FIELD IS OMITTED FROM THE REQUEST rather than sent whole. Nothing is sent
for it, so the vendor replaces nothing -- which is exactly the state the Ironic control measured as
safe. The CR's own changes to that field wait for a successful read, and the resource says so.

ONE LEVEL DEEP. The overlay replaces whole values under the field's top-level keys and does not
recurse, matching the detection side's depth so the two cannot disagree about which losses they
cover. An ARRAY is out of scope: merging one needs a notion of element identity, and declaring a
field here that holds an array is a no-op rather than a silent positional merge.

THERE IS NO WAY TO SAY "DELETE THIS KEY". A key the CR used to carry and no longer does is left at
the vendor's value, deliberately: inferring deletion from absence is the inference that produced
this defect.<br/>
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


### RestDefinition.spec.resource.verbsDescription[index].correlation
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindex)</sup></sup>



Correlation is REQUIRED for, and permitted only on, a 'dispatch' action. See the Correlation type.

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
        <td><b>echoedAt</b></td>
        <td>string</td>
        <td>
          EchoedAt is the path, PER ITEM of that collection, at which the token comes back — e.g.
".inputs.correlationId". Uses the same path dialect as itemsPath and async.poll.statusPath.

Whether a given vendor really echoes the token is exactly what this field forces the author to
establish before the RestDefinition is accepted, instead of discovering it as a resource that
reports Ready and never converges.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>fromSpec</b></td>
        <td>string</td>
        <td>
          FromSpec is the dot-path of the spec field the CR supplies as the correlation token, e.g.
"inputs.correlationId". It must resolve to a field that exists in the generated spec schema;
generation refuses otherwise, since a token the CR cannot carry can never be matched.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobserve">observe</a></b></td>
        <td>object</td>
        <td>
          Observe is the collection endpoint in which the invocation becomes visible.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelation)</sup></sup>



Observe is the collection endpoint in which the invocation becomes visible.

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
        <td><b>method</b></td>
        <td>enum</td>
        <td>
          Method for the collection read. POST is allowed because some APIs expose search that way.<br/>
          <br/>
            <i>Enum</i>: GET, POST<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>path</b></td>
        <td>string</td>
        <td>
          Path of the collection endpoint, written exactly as the OAS document declares it (paths are matched
by exact string, so a differing parameter name is a miss).<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>itemsPath</b></td>
        <td>string</td>
        <td>
          ItemsPath names the property holding the collection when the response is an ENVELOPE rather than a
bare array — e.g. "workflow_runs" for {"total_count": 3, "workflow_runs": [...]}.

Same rule, and the same reason, as the findby itemsPath: optional because the common envelope has
exactly one array property, required as soon as there is more than one, because guessing searches
the wrong list and reports not-found — which here means firing the dispatch a second time.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepagination">pagination</a></b></td>
        <td>object</td>
        <td>
          Pagination walks the collection page by page while looking for the echoed token.

Absent means a SINGLE page, which is what this verb did before 0.73.0. That was a correctness
problem and not only a scale one: a dispatch whose echo had fallen past page one was reported
ABSENT, and on a dispatch kind not-found is what makes the controller fire the invocation again.
So a collection longer than one page could re-run something that had already happened.

The walk is the same one a paginated findby uses, so the same strategies, the same bounds and the
same three-valued outcome apply — in particular, exhausting a bound is reported as its own outcome
and NEVER as absence.<br/>
          <br/>
            <i>Validations</i>:<li>self.type == 'continuationToken' ? has(self.continuationToken) : true: continuationToken configuration must be provided when type is 'continuationToken'</li><li>self.type == 'pageNumber' ? has(self.pageNumber) : true: pageNumber configuration must be provided when type is 'pageNumber'</li><li>self.type == 'continuationToken' ? !has(self.pageNumber) : true: pageNumber must not be set when type is 'continuationToken'</li><li>self.type == 'pageNumber' ? !has(self.continuationToken) : true: continuationToken must not be set when type is 'pageNumber'</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobservequeriesindex">queries</a></b></td>
        <td>[]object</td>
        <td>
          Queries are static query parameters sent on EVERY page of the correlation walk, including after
the pagination token is appended.

WHY THIS EXISTS. A vendor can make its pagination MODE depend on a query parameter. Zendesk
switches to cursor pagination only when `page[size]` is sent: without it the response carries no
`meta.has_more` or `meta.after_cursor`, so a declared continuationToken pagination has nothing to
follow and the walk sees one page. A dispatch kind whose correlation token is not on that page can
then never reach Ready, and files nothing either -- with no error on the CR (issue 719).

ON EVERY PAGE is the load-bearing half. The obvious implementation threads these into the first
request and lets pagination build the rest, which drops them from page 2 onward -- and on such a
vendor the walk silently reverts to offset mode mid-walk, so the token path disappears and the walk
stops after one page with no error. It works here because WalkPaginated reuses one call
configuration for every page and the paginator only ADDS its token to the existing query
(continuation_token.go: `q := req.URL.Query()`, then `q.Set`), rather than replacing it.

A MISSPELLED KEY IS REFUSED, NOT PRUNED, which is why this reuses QueryParam rather than a map:
`name` and `value` are both required, so `{nmae: "x"}` prunes to `{}` at admission and then fails
the required check. An open map would accept the typo and drop it silently -- the shape that made
this field necessary in the first place, since `queries` on a verb was pruned with only a helm
warning on 0.73.0.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobserveresponsetransform">responseTransform</a></b></td>
        <td>object</td>
        <td>
          ResponseTransform reshapes EACH ITEM of the collection before echoedAt is evaluated against it,
exactly as a findby verb's responseTransform does.

Without it, echoedAt has to address the vendor's raw item shape — and it is a dot-path, which
cannot select one element of an array by a field value. A token carried in a custom-fields list
(Zendesk: `custom_fields: [{id: 31750115749148, value: "<token>"}]`) is unreachable without first
reshaping the item, which is why this exists rather than making echoedAt an expression: one
mechanism, applied where findby already applies it, instead of a second path dialect.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.inline) != has(self.ref): exactly one of inline or ref must be set</li><li>!has(self.entrypoint) || has(self.ref): entrypoint is only valid together with ref</li>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe.pagination
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelationobserve)</sup></sup>



Pagination walks the collection page by page while looking for the echoed token.

Absent means a SINGLE page, which is what this verb did before 0.73.0. That was a correctness
problem and not only a scale one: a dispatch whose echo had fallen past page one was reported
ABSENT, and on a dispatch kind not-found is what makes the controller fire the invocation again.
So a collection longer than one page could re-run something that had already happened.

The walk is the same one a paginated findby uses, so the same strategies, the same bounds and the
same three-valued outcome apply — in particular, exhausting a bound is reported as its own outcome
and NEVER as absence.

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
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationcontinuationtoken">continuationToken</a></b></td>
        <td>object</td>
        <td>
          Configuration for 'continuationToken' pagination. Required if type is 'continuationToken'.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationpagenumber">pageNumber</a></b></td>
        <td>object</td>
        <td>
          Configuration for 'pageNumber' pagination. Required if type is 'pageNumber'.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe.pagination.continuationToken
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepagination)</sup></sup>



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
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationcontinuationtokenrequest">request</a></b></td>
        <td>object</td>
        <td>
          Request: defines how to include the pagination token in the API request.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationcontinuationtokenresponse">response</a></b></td>
        <td>object</td>
        <td>
          Response: defines how to extract the pagination token from the API response.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>maxPages</b></td>
        <td>integer</td>
        <td>
          MaxPages bounds the walk. OPTIONAL, and absent means UNBOUNDED -- deliberately unlike
pageNumber's, which is required.

The asymmetry is not an oversight, and it is not consistency for its own sake being sacrificed.
The two strategies have different end signals. This one's default rule is token ABSENCE: the
server stops sending a cursor and the walk ends, which terminates correctly with no bound at all.
A header-token API is the common case and four live findby kinds rely on exactly that today.

A bound is needed only where absence is NOT the terminator -- Zendesk returns a cursor on the last
page and signals the end solely through meta.has_more, so without hasMorePath the absence rule
never fires. Those kinds declare both.

Making it required was tried and reverted before release: it rejects an unchanged chart's apply,
and a default instead would cap walks that work unbounded today -- turning an admission failure
into a silent runtime one that appears only on large tenants.

When set, exhausting it is reported as its own outcome, NEVER as not-found, exactly as in
pageNumber: "I scanned N pages and did not conclude" is not "it does not exist". For a dispatch
kind the difference is a re-run, because the remedy for not-found is to invoke again.<br/>
          <br/>
            <i>Minimum</i>: 1<br/>
            <i>Maximum</i>: 1000<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe.pagination.continuationToken.request
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationcontinuationtoken)</sup></sup>



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


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe.pagination.continuationToken.response
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationcontinuationtoken)</sup></sup>



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
      </tr><tr>
        <td><b>hasMorePath</b></td>
        <td>string</td>
        <td>
          HasMorePath names a BODY field whose boolean value says whether another page exists. Optional:
when it is absent the end of the collection is token ABSENCE, which is this strategy's original
and still-common rule.

It exists because token absence is not every API's terminator, and relying on it where it is not
documented is relying on undocumented behaviour. Zendesk's pagination reference documents exactly
one end signal -- repeat "until the meta[has_more] property is false" -- and says nothing about
whether after_cursor is present, null or absent on the last page. Against such an API the
absence rule never fires and the walk runs to maxPages on every call.

When declared it is AUTHORITATIVE: has_more false ends the walk even if a token is still present,
which is the Zendesk case and the reason this field exists.

has_more true with NO token is Indeterminate, not a continuation. Sending the next request without
a token re-fetches the FIRST page, which would loop to maxPages and then blame the operator's cap
for a server contradiction; reporting it names the real problem -- the two signals disagree.

A declared signal that cannot be read, or that does not hold a boolean, also yields Indeterminate,
never Exhausted -- the author said this is how you will know when to stop, so its absence means we
do not know.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe.pagination.pageNumber
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepagination)</sup></sup>



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
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationpagenumberrequest">request</a></b></td>
        <td>object</td>
        <td>
          Request: how the page number (and optionally the page size) are sent.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationpagenumberresponse">response</a></b></td>
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


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe.pagination.pageNumber.request
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationpagenumber)</sup></sup>



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


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe.pagination.pageNumber.response
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationpagenumber)</sup></sup>



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
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationpagenumberresponsebody">body</a></b></td>
        <td>object</td>
        <td>
          Body recognises the end by comparing against a total the API reports.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.totalPagesPath) != has(self.totalItemsPath): declare exactly one of totalPagesPath or totalItemsPath</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationpagenumberresponseheader">header</a></b></td>
        <td>object</td>
        <td>
          Header recognises "more pages exist" by matching a response header, e.g. Link containing
rel="next". Covers GitHub, GitLab and Jira without the engine knowing any of their names.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe.pagination.pageNumber.response.body
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationpagenumberresponse)</sup></sup>



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


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe.pagination.pageNumber.response.header
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelationobservepaginationpagenumberresponse)</sup></sup>



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


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe.queries[index]
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelationobserve)</sup></sup>



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


### RestDefinition.spec.resource.verbsDescription[index].correlation.observe.responseTransform
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexcorrelationobserve)</sup></sup>



ResponseTransform reshapes EACH ITEM of the collection before echoedAt is evaluated against it,
exactly as a findby verb's responseTransform does.

Without it, echoedAt has to address the vendor's raw item shape — and it is a dot-path, which
cannot select one element of an array by a field value. A token carried in a custom-fields list
(Zendesk: `custom_fields: [{id: 31750115749148, value: "<token>"}]`) is unreachable without first
reshaping the item, which is why this exists rather than making echoedAt an expression: one
mechanism, applied where findby already applies it, instead of a second path dialect.

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
            <i>Validations</i>:<li>self.type == 'secretRef' ? has(self.secretRef) : true: secretRef must be set when type is 'secretRef'</li><li>self.type == 'dependentLookup' ? has(self.dependentLookup) : true: dependentLookup must be set when type is 'dependentLookup'</li><li>self.type == 'secretRef' ? !has(self.dependentLookup) : true: dependentLookup cannot be set when type is 'secretRef'</li><li>self.type == 'dependentLookup' ? !has(self.secretRef) : true: secretRef cannot be set when type is 'dependentLookup'</li>
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
            <i>Enum</i>: secretRef, dependentLookup<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexresolverdependentlookup">dependentLookup</a></b></td>
        <td>object</td>
        <td>
          DependentLookup substitutes the result of a named pre-call read (used when type is
'dependentLookup'). The named lookup is declared in Resource.DependentLookups.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexresolversecretref">secretRef</a></b></td>
        <td>object</td>
        <td>
          SecretRef substitutes a Kubernetes Secret's value for the field (used when type is 'secretRef'). The
Secret is always read from the Custom Resource instance's own namespace — there is no field to name a
different one.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexresolverwhen">when</a></b></td>
        <td>object</td>
        <td>
          When makes this resolver conditional on the Custom Resource carrying a field. Without it a resolver
is unconditional, which is the behaviour every definition has today.

AT THE RESOLVER LEVEL, not inside secretRef, and deliberately so: the guard is about whether this
MAPPING applies, which is independent of which resolver kind supplies the value. DependentLookupRef
keeps its own `when` (LookupWhen) at its existing path, so no published field moves.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].fieldMapping[index].resolver.dependentLookup
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexresolver)</sup></sup>



DependentLookup substitutes the result of a named pre-call read (used when type is
'dependentLookup'). The named lookup is declared in Resource.DependentLookups.

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
          Name is the dependentLookups entry to substitute from.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexresolverdependentlookupwhen">when</a></b></td>
        <td>object</td>
        <td>
          When guards the lookup, so it is skipped when the value is already available. Without a guard, a CR
that supplies the value explicitly would still pay for the read and then have the result discarded.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.verbsDescription[index].fieldMapping[index].resolver.dependentLookup.when
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexresolverdependentlookup)</sup></sup>



When guards the lookup, so it is skipped when the value is already available. Without a guard, a CR
that supplies the value explicitly would still pay for the read and then have the result discarded.

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
        <td><b>customResourceFieldAbsent</b></td>
        <td>string</td>
        <td>
          CustomResourceFieldAbsent runs the lookup only when this JSONPath is absent or null in the Custom
Resource, so an explicitly supplied value wins and costs no API calls.<br/>
        </td>
        <td>true</td>
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


### RestDefinition.spec.resource.verbsDescription[index].fieldMapping[index].resolver.when
<sup><sup>[↩ Parent](#restdefinitionspecresourceverbsdescriptionindexfieldmappingindexresolver)</sup></sup>



When makes this resolver conditional on the Custom Resource carrying a field. Without it a resolver
is unconditional, which is the behaviour every definition has today.

AT THE RESOLVER LEVEL, not inside secretRef, and deliberately so: the guard is about whether this
MAPPING applies, which is independent of which resolver kind supplies the value. DependentLookupRef
keeps its own `when` (LookupWhen) at its existing path, so no published field moves.

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
        <td><b>customResourceFieldPresent</b></td>
        <td>string</td>
        <td>
          CustomResourceFieldPresent resolves this mapping only when this path resolves to a non-null value on
the Custom Resource. When the path is absent or explicitly null, the whole mapping is SKIPPED: no
Secret is read and nothing is written to the request.

An explicit null counting as absent is the existing convention (see LookupWhen): it is how a CR says
"I am not supplying this".

THE GUARD PATH IS A SEPARATE READ from the resolver's own paths. If the guard path is ambiguous --
a [?key=value] selector matching more than one element -- that is an ERROR, not a skip. A skip would
turn a malformed resource into one that silently reconciles without the guarded field, which is the
exact outcome the guard exists to prevent.

MinLength=1 BECAUSE AN EMPTY PATH SILENTLY DISABLES THE GUARD. The field is +required, so `when: {}`
is already refused -- but `customResourceFieldPresent: ""` was admitted, and both evaluation sites
treat an empty path as "no guard" and resolve unconditionally. So a templating accident that renders
an empty string produced a definition that validates, shows the guard on `kubectl get`, and behaves
as though it were absent. A reviewer found this; no test covered it, because an empty path reaches
exactly the same code path as a nil guard.

Safe as a tightening: this field has never shipped, so no existing definition can carry an empty one.<br/>
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
ABSENT MEANS A SINGLE PAGE, AND THAT IS A CORRECTNESS SETTING RATHER THAN A SCALE ONE. The same
statement is on correlation.observe.pagination and it was missing here, which is the asymmetry that
cost a consumer an investigation: "no pagination will be used" reads as a performance note.

What it actually means: findby issues ONE request and matches within that response
(restclient.go refuses a nil paginator and calls CallFindBySingle). A resource that exists but falls
beyond the vendor's default page is reported as NOT FOUND -- and on a findby that is worse than the
dispatch case, because the listing succeeded. "Listed, nothing matched" is a SYNTHESISED not-found,
the create then stamps krateo.io/external-create-pending, and recovery asks the same findby, which
answers the same way. The CR latches at CreateOutcomeUnknown and needs a human (issue 818).

MEASURED: a realm with 105 users, where the vendor's default page is 100. A managed user sorting
after the page boundary was created at the vendor exactly once, reported absent, and stuck;
the control sorting first was Ready. No duplicate -- the resource exists and the CR cannot see it.

DECLARING PAGINATION IS NOT USUALLY THE RIGHT FIX. Narrowing the listing is: if the operation
accepts a query parameter that identifies the resource, declare it and the response is 0 or 1 items
regardless of collection size. Walking 100 items to find one is a worse answer than asking for one,
and it stays correct when the collection reaches 10,000. Pagination is for collections that cannot
be narrowed.

AND A NARROWING PARAMETER MUST BE EXACT. A substring filter returns several items, which issue 444
made a REFUSAL rather than an adoption of the first -- so a findby narrowed by a prefix match works
until two resources share that prefix, and then stops working. Where the vendor offers an
exact-match flag, send it as a constant through the Configuration CR's configurationFields rather
than as a spec field: configurationFields wins over a spec field of the same name, so the behaviour
cannot be switched off per resource.

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
      </tr><tr>
        <td><b>maxPages</b></td>
        <td>integer</td>
        <td>
          MaxPages bounds the walk. OPTIONAL, and absent means UNBOUNDED -- deliberately unlike
pageNumber's, which is required.

The asymmetry is not an oversight, and it is not consistency for its own sake being sacrificed.
The two strategies have different end signals. This one's default rule is token ABSENCE: the
server stops sending a cursor and the walk ends, which terminates correctly with no bound at all.
A header-token API is the common case and four live findby kinds rely on exactly that today.

A bound is needed only where absence is NOT the terminator -- Zendesk returns a cursor on the last
page and signals the end solely through meta.has_more, so without hasMorePath the absence rule
never fires. Those kinds declare both.

Making it required was tried and reverted before release: it rejects an unchanged chart's apply,
and a default instead would cap walks that work unbounded today -- turning an admission failure
into a silent runtime one that appears only on large tenants.

When set, exhausting it is reported as its own outcome, NEVER as not-found, exactly as in
pageNumber: "I scanned N pages and did not conclude" is not "it does not exist". For a dispatch
kind the difference is a re-run, because the remedy for not-found is to invoke again.<br/>
          <br/>
            <i>Minimum</i>: 1<br/>
            <i>Maximum</i>: 1000<br/>
        </td>
        <td>false</td>
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
      </tr><tr>
        <td><b>hasMorePath</b></td>
        <td>string</td>
        <td>
          HasMorePath names a BODY field whose boolean value says whether another page exists. Optional:
when it is absent the end of the collection is token ABSENCE, which is this strategy's original
and still-common rule.

It exists because token absence is not every API's terminator, and relying on it where it is not
documented is relying on undocumented behaviour. Zendesk's pagination reference documents exactly
one end signal -- repeat "until the meta[has_more] property is false" -- and says nothing about
whether after_cursor is present, null or absent on the last page. Against such an API the
absence rule never fires and the walk runs to maxPages on every call.

When declared it is AUTHORITATIVE: has_more false ends the walk even if a token is still present,
which is the Zendesk case and the reason this field exists.

has_more true with NO token is Indeterminate, not a continuation. Sending the next request without
a token re-fetches the FIRST page, which would loop to maxPages and then blame the operator's cap
for a server contradiction; reporting it names the real problem -- the two signals disagree.

A declared signal that cannot be read, or that does not hold a boolean, also yields Indeterminate,
never Exhausted -- the author said this is how you will know when to stop, so its absence means we
do not know.<br/>
        </td>
        <td>false</td>
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


### RestDefinition.spec.resource.dependentLookups[index]
<sup><sup>[↩ Parent](#restdefinitionspecresource)</sup></sup>



DependentLookup is a named, pre-call read against any endpoint in the document, whose selected value is
substituted into a field of the main request by a fieldMapping entry naming it.

Exactly one of 'select' or 'match' is configured: 'select' picks a value out of a single-object response
(GitRef's two reads), 'match' finds one item in a list response and takes a field off it (Keycloak's
alias -> id).

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
        <td><b>method</b></td>
        <td>enum</td>
        <td>
          Method is the HTTP method of the lookup's operation.

GET only, deliberately. A lookup is a READ, and the APIs that express a search as POST need a
request body, which there is no field here to declare. Admitting POST without that would ship a
discoverable value that cannot work -- the mistake apiLookup made. Neither known consumer needs it:
GitRef chains two GETs, KeycloakClient lists with a GET. Widen it together with a way to declare
the body, when something actually needs one.<br/>
          <br/>
            <i>Enum</i>: GET<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          Name identifies this lookup. A fieldMapping resolver references it by this name, as does a later
lookup's parameter via 'fromLookup'.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>path</b></td>
        <td>string</td>
        <td>
          Path is the lookup's path, which MUST exist in the document under Method exactly as a verb's does --
parameters are resolved against the document, so a path that is not declared there has no parameter
schema and cannot be bound.

This is a sharper constraint than it looks when transcribing an API's docs. oasgen-provider#120
writes GitHub's git-ref read in prose as /repos/{owner}/{repo}/git/ref/heads/{default_branch}, but
GitHub's actual operation is GET /repos/{owner}/{repo}/git/ref/{ref} -- ONE parameter, whose value
must be formatted "heads/<branch>". The prose path would not resolve; the real one needs the prefix
supplied, which is what a parameter's valueMapping is for.

A document also has to declare the operation at all. Confirmed with the GitHub KOG: none of the five
OAS assets it ships declares any git-ref operation today -- the only git/refs occurrences are
git_refs_url hypermedia example strings inside the Repository schema. Adding the operation to the
document is a prerequisite for that consumer, separate from this field existing.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourcedependentlookupsindexmatch">match</a></b></td>
        <td>object</td>
        <td>
          Match finds exactly one item in a list response and takes a field off it.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>oasPath</b></td>
        <td>string</td>
        <td>
          OASPath overrides the document this lookup resolves against, exactly as a verb's does (#108). A
lookup against a different service than the resource's own verbs is the case this exists for.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourcedependentlookupsindexparametersindex">parameters</a></b></td>
        <td>[]object</td>
        <td>
          Parameters binds the lookup's OWN path and query parameters, from the Custom Resource or from an
earlier lookup's result.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>select</b></td>
        <td>string</td>
        <td>
          Select is a gojq program run against the lookup's response body, yielding the single value to
substitute, e.g. '.default_branch' or '.object.sha'.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.dependentLookups[index].match
<sup><sup>[↩ Parent](#restdefinitionspecresourcedependentlookupsindex)</sup></sup>



Match finds exactly one item in a list response and takes a field off it.

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
        <td><b>equalsFromCustomResource</b></td>
        <td>string</td>
        <td>
          EqualsFromCustomResource is a JSONPath into the Custom Resource yielding the value to match.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>selectField</b></td>
        <td>string</td>
        <td>
          SelectField is the field taken off the matched item, e.g. 'id'.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>whereField</b></td>
        <td>string</td>
        <td>
          WhereField is the field on each item compared against the Custom Resource value, e.g. 'alias'.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>itemsAt</b></td>
        <td>string</td>
        <td>
          ItemsAt is a gojq program yielding the array to search. Defaults to '.' for a response that is
itself a bare array.<br/>
          <br/>
            <i>Default</i>: .<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>onMissing</b></td>
        <td>enum</td>
        <td>
          OnMissing selects what happens when no item matches.
  - "requeue" (default): the referent may still be converging -- a flow CR that has not been
    created yet is the expected case, not an error.
  - "error": no match is a permanent misconfiguration.<br/>
          <br/>
            <i>Enum</i>: requeue, error<br/>
            <i>Default</i>: requeue<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.dependentLookups[index].parameters[index]
<sup><sup>[↩ Parent](#restdefinitionspecresourcedependentlookupsindex)</sup></sup>



LookupParameter binds one of a lookup's own parameters. The value comes EITHER from the Custom Resource
or from an earlier lookup's result, never both.

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
        <td><b>fromLookup</b></td>
        <td>string</td>
        <td>
          FromLookup names an EARLIER dependentLookups entry whose result supplies the value.

"Earlier" is load-bearing and enforced at the Resource level: a lookup may only reference one
declared above it. That makes a cycle unrepresentable rather than detected, so there is no solver
and no runtime cycle check to get wrong.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inCustomResource</b></td>
        <td>string</td>
        <td>
          InCustomResource is a JSONPath into the Custom Resource supplying the value.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inPath</b></td>
        <td>string</td>
        <td>
          InPath selects a path parameter of the lookup's operation.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>inQuery</b></td>
        <td>string</td>
        <td>
          InQuery selects a query parameter of the lookup's operation.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourcedependentlookupsindexparametersindexvaluemapping">valueMapping</a></b></td>
        <td>object</td>
        <td>
          ValueMapping transforms the value before it is bound, using the same tiers as a fieldMapping entry.
The jq tier is what supplies a prefix a declared path needs but the source value does not carry,
e.g. '"heads/" + .' for GitHub's {ref}.<br/>
          <br/>
            <i>Validations</i>:<li>self.type == 'alias' ? has(self.aliases) : true: aliases must be set when type is 'alias'</li><li>self.type == 'jq' ? has(self.jq) : true: jq must be set when type is 'jq'</li>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### RestDefinition.spec.resource.dependentLookups[index].parameters[index].valueMapping
<sup><sup>[↩ Parent](#restdefinitionspecresourcedependentlookupsindexparametersindex)</sup></sup>



ValueMapping transforms the value before it is bound, using the same tiers as a fieldMapping entry.
The jq tier is what supplies a prefix a declared path needs but the source value does not carry,
e.g. '"heads/" + .' for GitHub's {ref}.

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
        <td><b><a href="#restdefinitionspecresourcedependentlookupsindexparametersindexvaluemappingaliasesindex">aliases</a></b></td>
        <td>[]object</td>
        <td>
          Aliases is an explicit set of bidirectional CR<->API value pairs (used when type is 'alias').
On the request the CR value is rewritten to its apiValue; on the response the apiValue is rewritten
back to the CR value; any value without a matching pair passes through unchanged.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#restdefinitionspecresourcedependentlookupsindexparametersindexvaluemappingjq">jq</a></b></td>
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


### RestDefinition.spec.resource.dependentLookups[index].parameters[index].valueMapping.aliases[index]
<sup><sup>[↩ Parent](#restdefinitionspecresourcedependentlookupsindexparametersindexvaluemapping)</sup></sup>



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


### RestDefinition.spec.resource.dependentLookups[index].parameters[index].valueMapping.jq
<sup><sup>[↩ Parent](#restdefinitionspecresourcedependentlookupsindexparametersindexvaluemapping)</sup></sup>



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
        <td><b>generatorBuild</b></td>
        <td>string</td>
        <td>
          GeneratorBuild: the build identity of the oasgen-provider that last generated this
RestDefinition's CRD -- the string stamped in at link time via -X main.Build, the same one
SERVICE_VERSION carries.

Exists because none of the four hashes above covers the GENERATOR. oasHash covers the documents,
resourceHash covers spec.resource, digest covers the rendered managed resources and
authSecretDigest covers the referenced Secrets: all four describe the INPUT. Upgrading the
provider changes the FUNCTION applied to that input and leaves every one of them equal, so
Observe reported "up to date" and a release's generation fixes never reached a CRD that already
existed. Worse, the rendered-digest comparison DOES carry the new RDC image to the per-definition
Deployment -- so the runtime half of a release arrived and the generation half silently did not,
which is the hardest shape to notice because the upgrade visibly worked (#453).

Empty is meaningful and self-healing: a plain `go build` stamps nothing, so an unstamped binary
records "" and never triggers on it, and the next stamped build sees a difference and regenerates
once. That is correct rather than unfortunate -- the CRD really was last generated by a build
that cannot be identified.<br/>
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
      </tr><tr>
        <td><b>resourceHash</b></td>
        <td>string</td>
        <td>
          ResourceHash: a content hash of spec.resource at the last successful Create/Update.

The CRD is regenerated when the OAS content changes, which was sufficient while every input to
generation was immutable. additionalStatusFields is append-only rather than immutable (a KOG chart
adding a status field must be upgradable in place), and since #666 excludedSpecFields is fully mutable,
adding a status field must be upgradable in place), so a RestDefinition can now change in a way that
alters the generated schema while the OAS is byte-identical. Without this the apply would be accepted
and then do nothing -- the CRD keeping its old status schema, looking for all the world like it had
worked.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>schemaWarnings</b></td>
        <td>[]string</td>
        <td>
          SchemaWarnings: the generation and validation warnings produced by the CURRENT render of this
RestDefinition, as rendered messages.

These are deliberately non-fatal -- a RestDefinition carrying them is still Ready and still Synced,
because failing generation over a declaration that names a missing field would break working
installs. They are recorded here because the FACT is permanent while the channel that used to carry
it was not: they also go to a Warning Event, and the apiserver's default --event-ttl is one hour,
so a definition installed yesterday with broken declarations was indistinguishable from a correct
one through every cluster-visible channel (#462).

REWRITTEN ON EVERY RENDER, including to empty. A fixed declaration clears this rather than leaving a
stale complaint, which is the property that makes it worth reading at all.

"INCLUDING TO EMPTY" IS ABOUT THE FIELD AND NOT THE WIRE. An empty result is omitted from the
serialised object rather than written as `[]`, deliberately: `schemaWarnings: []` on every healthy
RestDefinition is noise in every `kubectl get -o yaml`.

SO AN ABSENT FIELD MEANS EITHER "this render produced no warnings" OR "this RestDefinition has not
been rendered by a version that writes the field", and the two cannot be told apart from this field
alone. Read status.generatorBuild alongside it when that distinction matters -- for example when
sweeping a fleet for definitions whose declarations need fixing (#492).<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>secretRefDigest</b></td>
        <td>string</td>
        <td>
          SecretRefDigest: a hash of the Secret names (grouped by namespace) currently referenced by a secretRef
fieldMapping resolver, across every instance of the GENERATED Kind.

WITHOUT THIS THE GRANT IS NEVER PROVISIONED. syncSecretRefRBAC runs only from Create and Update, and
the generated CRD is created INSIDE Create -- so Create's own collection always sees zero instances,
because no CR of the kind can exist before its CRD does. Observe must therefore notice when the
instances change, or nothing ever reaches Update. A consumer measured exactly this on a real cluster:
8 CRs across 7 namespaces, 6.5 minutes, zero Roles provisioned.

This is #473's lesson applied to the second caller: a digest answers "did the references change", and
the companion existence check in Observe answers "does the Role still exist", which a digest cannot.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>secretRefRBACNamespaces</b></td>
        <td>[]string</td>
        <td>
          SecretRefRBACNamespaces: the namespaces in which a namespace-scoped Role+RoleBinding currently grants
RDC's ServiceAccount read access to the Secrets named by a secretRef fieldMapping resolver, collected
across every instance of the GENERATED Kind (not the Configuration Kind -- see AuthSecretRBACNamespaces
for that one).

SEPARATE from AuthSecretRBACNamespaces, and the separation is load-bearing rather than tidiness.
SyncAuthSecretRBAC deletes the Role in every namespace that is in the caller's recorded set and no
longer in its collected set. Two callers sharing one record would each delete the other's namespaces on
every reconcile, and the symptom -- a credential readable one moment and Forbidden the next -- would
pass every unit test, because a single-caller test cannot observe the other caller. The Role NAMES are
distinct for the same reason (see deploy.SecretRefRoleName).<br/>
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
