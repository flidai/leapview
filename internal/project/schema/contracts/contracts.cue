package contracts

#Identifier: =~"^[A-Za-z_][A-Za-z0-9_]*$"
#ObjectID:   =~"^[A-Za-z_][A-Za-z0-9_-]*$"
#ResourceID: =~"^[A-Za-z0-9][A-Za-z0-9_.:-]*$"
#ResourceName: =~"^[A-Za-z_][A-Za-z0-9_.-]*$"
#FieldRef:   =~"^[A-Za-z_][A-Za-z0-9_]*\\.[A-Za-z_][A-Za-z0-9_]*$"
#AnyObject: {
	[string]: _
}

#NoCredentials: close({
	provider!: "none"
})

#EnvCredentials: close({
	provider!: "env"
	secret!:   string
})

#AmbientCredentials: close({
	provider!:    "ambient"
	region?:      string
	endpoint?:    string
	accountName?: string
})

#APIVersion: "leapview.dev/v1"

// Connector identities are generated from the TypeSpec connection contract;
// keeping the closed set here makes the current public registry visible to
// CUE consumers as well.
#ConnectorKind: "managed" | "s3" | "r2" | "gcs" | "http" | "azure_blob" | "postgres" | "mysql" | "sqlite" | "ducklake" | "quack"

#Provenance: close({
	origin?: string
	path?:   string
	source?: string
})

#Metadata: close({
	id!:            #ResourceID
	name!:          #ResourceName
	displayName?:   string
	description?:   string
	owner?:         string
	domain?:        string
	tags?:          [...string]
	documentation?: string
	provenance?:    #Provenance
})

#IncludeList: close({
	include!: [...string]
})

#Project: close({
	apiVersion!: #APIVersion
	kind!:       "Project"
	metadata!:   #Metadata
	spec!: close({
		connections!: #IncludeList
		sources!:     #IncludeList
		models!:         #IncludeList
		semanticModels!: #IncludeList
		pipelines!:      #IncludeList
		dashboards!:     #IncludeList
		access!:         #IncludeList
		publications?:   #IncludeList
	})
})

#GroupResource: close({
	apiVersion!: #APIVersion
	kind!:       "Group"
	metadata!:   #Metadata
	spec!: #Group
})

#RoleBindingResource: close({
	apiVersion!: #APIVersion
	kind!:       "RoleBinding"
	metadata!:   #Metadata
	spec!: #RoleBinding
})

#Group: close({
	description?: string
	members?: [...close({
		principalId?: #ResourceID
		email?:       string
		displayName?: string
	})]
})

#AccessSubject: close({
	kind!:        "principal" | "group" | "service_principal"
	principalId?: #ResourceID
	email?:       string
	displayName?: string
	group?:       #ResourceID
})

#RoleBinding: close({
	role!: "owner" | "admin" | "deployer" | "data_deployer" | "contributor" | "editor" | "member" | "viewer"
	subject!: #AccessSubject
})

#ResourceRef: close({
	id!:   #ResourceID
	kind!: "project" | "connection" | "source" | "model" | "semantic_model" | "pipeline" | "dashboard"
})

#Capability: "PROJECT_ADMIN" | "RESOURCE_USE" | "RESOURCE_READ" | "RESOURCE_EDIT" | "RESOURCE_MANAGE" | "RESOURCE_SHARE" | "RESOURCE_PUBLISH"

#DataPolicyTarget: close({
	kind!: "semantic_model" | "source" | "model"
	id!:   #ResourceID
})

#DataPolicySubject: close({
	kind!:        "principal" | "group" | "service_principal" | "dashboard_publication"
	principalId?: #ResourceID
	email?:       string
	displayName?: string
	group?:       #ResourceID
	publication?: #ResourceID
})

#GrantResource: close({
	apiVersion!: #APIVersion
	kind!:       "Grant"
	metadata!:   #Metadata
	spec!: close({
		object!:    #ResourceRef
		subject!:   #AccessSubject
		capability!: #Capability
	})
})

#DataPolicyResource: close({
	apiVersion!: #APIVersion
	kind!:       "DataPolicy"
	metadata!:   #Metadata
	spec!: close({
		object!:     #DataPolicyTarget
		subject?:    #DataPolicySubject
		policyType!: "row_filter" | "column_mask"
		expression!: #AnyObject
	})
})

#SemanticModelResource: close({
	apiVersion!: #APIVersion
	kind!:       "SemanticModel"
	metadata!:   #Metadata
	aiContext?:  #AIContext
	spec!:        #ProjectSemanticModelSpec
})

#DashboardPublicationResource: close({
	apiVersion!: #APIVersion
	kind!:       "DashboardPublication"
	metadata!:   #Metadata
	spec!: close({
		dashboard!:   #ResourceID
		defaultPage!: #ResourceID
		embedding!: close({
			allowedOrigins!: [...string]
		})
	})
})

#LogicalDataType: "String" | "Integer" | "Decimal" | "Float" | "Boolean" | "Date" | "Time" | "DateTime" | "DateTimeTz" | "Opaque"

#TimeGrain: "second" | "minute" | "hour" | "day" | "week" | "month" | "quarter" | "year"

#AIContext: close({
	instructions?: string
	synonyms?: [...string]
	examples?: [...string]
})

#SemanticDataset: close({
	model!: #ResourceName
	defaultTimeDimension?: #Identifier
	displayName?: string
	description?: string
	aiContext?: #AIContext
})

#NamedRelationshipEndpoint: close({
	dataset!: #Identifier
	entity!: #Identifier
})

#FieldsRelationshipEndpoint: close({
	dataset!: #Identifier
	fields!: [#Identifier, ...#Identifier]
})

#RelationshipEndpoint: #NamedRelationshipEndpoint | #FieldsRelationshipEndpoint

#Relationship: close({
	from!: #RelationshipEndpoint
	to!: #RelationshipEndpoint
	description?: string
	aiContext?: #AIContext
})

#DimensionBinding: close({
	field!: #FieldRef
	path?: [...#Identifier]
})

#TimeSemantics: close({
	nativeGrain!: #TimeGrain
	grains!: [#TimeGrain, ...#TimeGrain]
	calendar?: string
	timezone?: string
})

#SemanticDimension: close({
	label?: string
	description?: string
	aiContext?: #AIContext
	datatype!: #LogicalDataType
	time?: #TimeSemantics
	bindings!: close({
		[#Identifier]: #DimensionBinding
	})
})

#Literal: string | int | float | bool

#EqualsFilter: close({field!: #FieldRef, operator!: "equals", value!: #Literal, path?: [...#Identifier], aiContext?: #AIContext})
#NotEqualsFilter: close({field!: #FieldRef, operator!: "not_equals", value!: #Literal, path?: [...#Identifier], aiContext?: #AIContext})
#InFilter: close({field!: #FieldRef, operator!: "in", value!: [#Literal, ...#Literal], path?: [...#Identifier], aiContext?: #AIContext})
#NotInFilter: close({field!: #FieldRef, operator!: "not_in", value!: [#Literal, ...#Literal], path?: [...#Identifier], aiContext?: #AIContext})
#LessThanFilter: close({field!: #FieldRef, operator!: "less_than", value!: #Literal, path?: [...#Identifier], aiContext?: #AIContext})
#LessThanOrEqualFilter: close({field!: #FieldRef, operator!: "less_than_or_equal", value!: #Literal, path?: [...#Identifier], aiContext?: #AIContext})
#GreaterThanFilter: close({field!: #FieldRef, operator!: "greater_than", value!: #Literal, path?: [...#Identifier], aiContext?: #AIContext})
#GreaterThanOrEqualFilter: close({field!: #FieldRef, operator!: "greater_than_or_equal", value!: #Literal, path?: [...#Identifier], aiContext?: #AIContext})
#IsNullFilter: close({field!: #FieldRef, operator!: "is_null", path?: [...#Identifier], aiContext?: #AIContext})
#IsNotNullFilter: close({field!: #FieldRef, operator!: "is_not_null", path?: [...#Identifier], aiContext?: #AIContext})

#FilterLeaf: #EqualsFilter | #NotEqualsFilter | #InFilter | #NotInFilter | #LessThanFilter | #LessThanOrEqualFilter | #GreaterThanFilter | #GreaterThanOrEqualFilter | #IsNullFilter | #IsNotNullFilter
#FilterNode: #FilterLeaf | close({all!: [#FilterNode, ...#FilterNode]}) | close({any!: [#FilterNode, ...#FilterNode]}) | close({not!: #FilterNode})
#SemanticFilter: #FilterNode

#AggregateMetric: close({
	type!: "aggregate"
	dataset!: #Identifier
	aggregation!: "sum" | "count" | "count_distinct" | "avg" | "min" | "max"
	input!: close({field!: #FieldRef})
	where?: [#Identifier, ...#Identifier]
	empty?: "zero" | "null"
	timeDimension?: #Identifier
	label?: string
	description?: string
	aiContext?: #AIContext
	unit?: string
	format?: string
	hidden?: bool
})

#DerivedMetric: close({
	type!: "derived"
	expression!: string
	label?: string
	description?: string
	aiContext?: #AIContext
	unit?: string
	format?: string
	hidden?: bool
})

#RatioMetric: close({
	type!: "ratio"
	numerator!: #Identifier
	denominator!: #Identifier
	label?: string
	description?: string
	aiContext?: #AIContext
	unit?: string
	format?: string
	hidden?: bool
})

#Metric: #AggregateMetric | #DerivedMetric | #RatioMetric

#ProjectSemanticModelSpec: close({
	datasets!: close({
		[#Identifier]: #SemanticDataset
	})
	relationships?: close({
		[#Identifier]: #Relationship
	})
	dimensions?: close({
		[#Identifier]: #SemanticDimension
	})
	filters?: close({
		[#Identifier]: #SemanticFilter
	})
	metrics!: close({
		[#Identifier]: #Metric
	})
})
