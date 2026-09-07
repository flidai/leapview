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
