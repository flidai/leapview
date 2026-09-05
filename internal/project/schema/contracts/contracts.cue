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
