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

#ConnectionResource: close({
	apiVersion!: #APIVersion
	kind!:       "Connection"
	metadata!:   #Metadata
	spec!:        #Connection
})

#SourceResource: close({
	apiVersion!: #APIVersion
	kind!:       "Source"
	metadata!:   #Metadata
	spec!:        #Source
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
	role!: "owner" | "admin" | "deployer" | "contributor" | "editor" | "member" | "viewer" | "platform_admin"
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

#PipelineResource: close({
	apiVersion!: #APIVersion
	kind!:       "Pipeline"
	metadata!:   #Metadata
	spec!: close({
		semanticModel!: #ResourceID
		on?: close({
			schedule?: [...close({
				cron!:     string & !=""
				timezone?: string & !=""
			})]
		})
	})
})
#ModelResource: close({
	apiVersion!: #APIVersion
	kind!:       "Model"
	metadata!:   #Metadata
	spec!:        #Model
})

#SemanticModelResource: close({
	apiVersion!: #APIVersion
	kind!:       "SemanticModel"
	metadata!:   #Metadata
	spec!:        #ProjectSemanticModelSpec
})

#DashboardResource: close({
	apiVersion!: #APIVersion
	kind!:       "Dashboard"
	metadata!:   #Metadata
	spec!:        #DashboardSpec
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

#Connection: close({
	kind!:        "managed" | "s3" | "r2" | "gcs" | "http" | "azure_blob" | "postgres" | "mysql" | "sqlite" | "ducklake" | "quack"
	description?: string
	path?:        string
	root?:        string
	scope?:       string
	host?:        string
	port?:        int
	database?:    string
	username?:    string
	sslMode?:     string
	credentials?: #NoCredentials | #EnvCredentials | #AmbientCredentials
	options?:     #AnyObject
	defaults?: close({
		options?: #AnyObject
	})
})

#Source: close({
	format?:      "csv" | "json" | "parquet" | "excel" | "text" | "blob" | "vortex" | "delta" | "iceberg" | "lance"
	description?: string
	path?:        string
	connection!:  #ResourceID
	object?:      string
	options?:     #AnyObject
	fields?: close({
		[#Identifier]: close({
			type?:        string
			description?: string
		})
	})
})

#Model: close({
	kind?:   string
	source?: #ResourceID
	sources?: [...#ResourceID]
	sql?: string
	transform?: close({
		sql?: string
	})
	primaryKey!:  #Identifier
	grain?:       #Identifier
	fields?: close({
		[#Identifier]: close({
			label?:       string
			description?: string
			expr?:        string
			expression?:  string
			type?:        string
		})
	})
	description?: string
})

#ProjectSemanticModelSpec: close({
	tables!: [...#ResourceID]
	relationships?: [...#Relationship]
	dimensions?: close({
		[#Identifier]: #SemanticDimension
	})
	measures!: close({
		[#Identifier]: #Measure
	})
	metrics?: close({
		[#Identifier]: #Metric
	})
})

#Measure: close({
	fact!:        #Identifier
	label?:       string
	description?: string
	aggregation!: "sum" | "count" | "count_distinct" | "avg" | "min" | "max"
	input?: close({
		field?: #FieldRef
		expression?: string
	})
	filters?: [...close({
		field!: #FieldRef
		operator!: "equals" | "in" | "contains" | "starts_with" | "greater_than_or_equal" | "less_than"
		values!: [..._]
	})]
	empty!: "zero" | "null"
	unit?:        string
	format?:      string
	hidden?:      bool
})

#SemanticDimension: close({
	label?: string
	description?: string
	type!: "string" | "number" | "boolean" | "date" | "timestamp"
	grains?: [...("day" | "week" | "month" | "quarter" | "year")]
	bindings!: close({
		[#Identifier]: close({
			field!: #FieldRef
			path?: [...#Identifier]
		})
	})
})

#Metric: close({
	label?: string
	description?: string
	expression!: string
	unit?: string
	format?: string
	hidden?: bool
})

#Relationship: close({
	id!:          #Identifier
	description?: string
	from!:        #FieldRef
	to!:          #FieldRef
	cardinality!: "many_to_one" | "one_to_one"
})

#Dashboard: close({
	id!:             #ObjectID
	title!:          string
	description?:    string
	semantic_model!: #Identifier
	filters?: close({
		[#Identifier]: #FilterDefinition
	})
	filter_bindings?: close({[#Identifier]: #FilterBinding})
	filter_application?: #FilterApplication
	visuals!: close({
		[#Identifier]: #Visual
	})
	pages!: [...#Page]
})

#DashboardSpec: close({
	appearance?: close({
		icon?:  #ObjectID | "default"
		color?: "gray" | "blue" | "green" | "yellow" | "orange" | "red" | "purple" | "pink" | "coral" | "default"
	})
	semanticModel!: #ResourceID
	filters?: close({
		[#Identifier]: #FilterDefinition
	})
	filter_bindings?: close({[#Identifier]: #FilterBinding})
	filter_application?: #FilterApplication
	visuals!: close({
		[#Identifier]: #Visual
	})
	pages!: [...#Page]
})

#FilterDefinition: close({
	label!:       string
	description?: string
	field!:       #FieldRef | #Identifier
	fact?:        #Identifier
	predicates!: [...#FilterPredicate]
	options?: #FilterOptionSource
	formatting?: close({
		pattern?: string
		unit?:    string
	})
})

#FilterPredicate: close({
	kind!: "null_check" | "set" | "comparison" | "range" | "relative_period"
	operators?: [...("is_null" | "is_not_null" | "in" | "not_in" | "equals" | "not_equals" | "contains" | "not_contains" | "starts_with" | "ends_with" | "greater_than" | "greater_than_or_equal" | "less_than" | "less_than_or_equal")]
})

#FilterOptionSource: close({
	kind?:  "static" | "distinct"
	limit?: int & >=0 & <=500
	values?: [...close({
		value!: #FilterValue
		label!: string
	})]
})

#FilterBinding: close({
	filter!:          #Identifier
	default?:         #FilterExpression
	selection?: close({
		mode?:                "single" | "multiple"
		max_selected_values?: int & >=0
	})
	reader_editable?: bool
	url?: close({
		param?:    string
		encoding?: "typed_v1"
	})
	pane?: close({
		visible?: bool
		order?:   int
		label?:   string
	})
	targets?: close({
		include?: [...string]
		exclude?: [...string]
	})
	option_interactions?: close({
		include?: [...#FilterBindingRef]
		exclude?: [...#FilterBindingRef]
	})
})

#FilterApplication: close({
	mode!: "immediate" | "deferred"
})

#FilterBindingRef: close({
	scope!: "page" | "report"
	id!:    #Identifier
})

#FilterValue: close({kind!: "string", value!: string}) |
	close({kind!: "boolean", value!: bool}) |
	close({kind!: "integer" | "decimal" | "date" | "timestamp", value!: string})

#FilterExpression: close({kind!: "unfiltered"}) |
	close({
		kind!:     "null_check"
		operator!: "is_null" | "is_not_null"
	}) |
	close({
		kind!:     "set"
		operator!: "in" | "not_in"
		values!:   [...#FilterValue]
	}) |
	close({
		kind!:     "comparison"
		operator!: "equals" | "not_equals" | "contains" | "not_contains" | "starts_with" | "ends_with" | "greater_than" | "greater_than_or_equal" | "less_than" | "less_than_or_equal"
		value!:    #FilterValue
	}) |
	close({
		kind!:  "range"
		lower?: #FilterBound
		upper?: #FilterBound
	}) |
	close({
		kind!:            "relative_period"
		direction!:       "previous" | "current" | "next"
		count!:           int & >0
		unit!:            "minute" | "hour" | "day" | "week" | "month" | "quarter" | "year"
		include_current?: bool
		anchor!:          "current_time" | "first_available" | "last_available" | "fixed"
		anchor_value?:    #FilterValue
	})

#FilterBound: close({
	value!:     #FilterValue
	inclusive!: bool
})

#Visual: #CartesianVisual | #PointVisual | #ProportionalVisual | #HierarchyVisual | #PolarVisual | #GeographicVisual | #KPIVisual | #DataTableVisual | #MatrixVisual | #PivotVisual

#VisualCommon: {
	title?:            string
	subtitle?:         string
	description?:      string
	datasets?:         close({[string & !="" & !="primary"]: #VisualQuery})
	metadata?:         #VisualMetadataBindings
	interaction?:      null | #Interaction
	accessibility?: close({
		title?:            string
		description?:      string
		summary?:          string
		announce_changes?: bool
	})
	data_budget?: close({
		max_rows?:              int & >0
		required_completeness?: "complete" | "truncated" | "partial" | "empty"
	})
	calculations?: [...#VisualCalculation]
}

#VisualCalculationOrder: close({
	field!:     string & !=""
	direction?: "asc" | "desc"
})

#VisualCalculationLookup: close({
	field!: string & !=""
	value!: string
})

#VisualCalculation: close({
	id!:           =~"^[A-Za-z_][A-Za-z0-9_]*$"
	label?:        string & !=""
	template!:     "running_total" | "moving_average" | "difference" | "percentage_difference" | "percent_of_parent" | "percent_of_grand_total" | "rank" | "cumulative_contribution" | "lookup"
	source!:       string & !=""
	axis?:         "rows" | "columns" | "hierarchy" | "facets"
	order_by?:     [...#VisualCalculationOrder]
	partition_by?: [...(string & !="")]
	reset?:        "none" | "highest_parent" | "lowest_parent"
	window?:       int & >0
	offset?:       int & >0
	parent?:       string & !=""
	lookup?:       #VisualCalculationLookup
	hidden?:       bool
	format?:       "number" | "decimal" | "integer" | "percent" | "compact" | "currency"
})

#VisualTextBinding: close({
	dataset?:  string & !=""
	field!:    string & !=""
	reducer?:  "first" | "last" | "minimum" | "maximum" | "mean" | "median"
	prefix?:   string
	suffix?:   string
	fallback!: string & !=""
})

#VisualMetadataBindings: close({
	title?:       #VisualTextBinding
	subtitle?:    #VisualTextBinding
	description?: #VisualTextBinding
	summary?:     #VisualTextBinding
})

#PresentationCommon: {
	legend?:      "hidden" | "top" | "right" | "bottom" | "left"
	display_units?: "auto" | "none" | "thousands" | "millions" | "billions" | "trillions"
	show_labels?: bool
	labels?: #LabelPolicy
	conditional_formatting?: [...#ConditionalFormat]
}

#LabelPolicy: close({
	density?:          "hidden" | "automatic" | "dense" | "always"
	priority?:         [...("selected" | "anomaly" | "threshold")]
	max_characters?:   int & >=4 & <=200
	minimum_spacing?:  int & >=0 & <=64
	tooltip_fallback?: bool
})

#DecisionTone: "neutral" | "ink" | "success" | "warning" | "danger"
#ColorIntent: #DecisionTone | "accent" | "data_1" | "data_2" | "data_3" | "data_4" | "data_5" | "data_6" | "data_7" | "data_8"
#IconIntent: "circle" | "square" | "diamond" | "triangle_up" | "triangle_down" | "arrow_up" | "arrow_down" | "warning"

#ConditionalStyle: close({
	color?: #ColorIntent
	icon?:  #IconIntent
})

#ConditionalTarget: "mark_fill" | "mark_stroke" | "series_color" | "label_foreground" | "visual_background" | "cell_foreground" | "cell_background" | "kpi_value" | "icon"

#ConditionalFormatCommon: {
	id!:     string & !=""
	target!: #ConditionalTarget
	field!:  string & !=""
}

#ConditionalFormat: close({
	#ConditionalFormatCommon
	kind!:    "gradient"
	minimum!: number
	maximum!: number
	low!:     #ConditionalStyle
	high!:    #ConditionalStyle
	"null"!:  #ConditionalStyle
}) | close({
	#ConditionalFormatCommon
	kind!: "rules"
	rules!: [close({
		operator!: "less_than" | "less_or_equal" | "greater_than" | "greater_or_equal" | "equal" | "not_equal"
		value!:    number
		style!:    #ConditionalStyle
	}), ...close({
		operator!: "less_than" | "less_or_equal" | "greater_than" | "greater_or_equal" | "equal" | "not_equal"
		value!:    number
		style!:    #ConditionalStyle
	})]
	"null"!:   #ConditionalStyle
	default!:  #ConditionalStyle
}) | close({
	#ConditionalFormatCommon
	kind!:         "field"
	source_field!: string & !=""
	values!:       close({[string & !=""]: #ConditionalStyle})
	"null"!:       #ConditionalStyle
	default!:      #ConditionalStyle
})

#ReferenceValue: close({
	"number"!: number
}) | close({
	"text"!: string & !=""
}) | close({
	dataset?: string & !=""
	"field"!: string & !=""
	reducer?: "first" | "last" | "minimum" | "maximum" | "mean" | "median"
})

#CartesianAxis: close({
	id!:            "x" | "primary_y" | "secondary_y"
	title?:         string
	scale?:         "automatic" | "linear" | "log"
	zero?:          "automatic" | "include" | "exclude"
	minimum?:       number
	maximum?:       number
	unit?:          string
	display_units?: "auto" | "none" | "thousands" | "millions" | "billions" | "trillions"
	tick_density?:  "automatic" | "sparse" | "normal" | "dense"
})

#ReferenceLine: close({
	id!:    string & !=""
	axis!:  "x" | "primary_y" | "secondary_y"
	value!: #ReferenceValue
	label?: string
	tone?:  #DecisionTone
})

#ReferenceBand: close({
	id!:    string & !=""
	axis!:  "x" | "primary_y" | "secondary_y"
	from!:  #ReferenceValue
	to!:    #ReferenceValue
	label?: string
	tone?:  #DecisionTone
})

#EventAnnotation: close({
	id!:          string & !=""
	axis!:        "x"
	value!:       #ReferenceValue
	label!:       string & !=""
	description?: string
	tone?:        #DecisionTone
})

#CartesianPresentation: close({
	#PresentationCommon
	stacked?:       bool
	smooth?:        bool
	show_symbols?:  bool
	data_zoom?:     bool
	area?:          bool
	step?:          bool
	orientation?:   "horizontal" | "vertical"
	label_position?: "automatic" | "inside" | "outside" | "top"
	symbol_size?:    number & >0
	histogram_bins?: int & >0
	series_types?: close({[string]: "line" | "area" | "bar" | "column"})
	dual_axis?: bool
	axes?:              [...#CartesianAxis]
	reference_lines?:   [...#ReferenceLine]
	reference_bands?:   [...#ReferenceBand]
	event_annotations?: [...#EventAnnotation]
	tooltip?:            [...string & !=""]
	stacking?:           "none" | "normal" | "percent"
	series_order?:       [...string & !=""]
	series_colors?:      close({[string & !=""]: #ColorIntent})
})

#ProportionalPresentation: close({
	#PresentationCommon
	orientation?:   "horizontal" | "vertical"
	rose?:          bool
	center_label?:  string
	label_position?: "automatic" | "inside" | "outside" | "top"
	inner_radius?: number & >=0 & <=1
	outer_radius?: number & >0 & <=1
	align?: "left" | "center" | "right"
	sort?: "ascending" | "descending"
})

#HierarchyPresentation: close({
	#PresentationCommon
	orientation?:  "horizontal" | "vertical"
	initial_depth?: int & >=0
	roam?:          bool
	layout?:        "standard" | "circular"
	breadcrumb?:    bool
	node_gap?:      number & >=0
	curveness?:     number & >=0 & <=1
	focus?:         "none" | "adjacency"
})

#Threshold: close({value!: number, tone!: "neutral" | "ink" | "success" | "warning" | "danger"})

#PolarPresentation: close({
	#PresentationCommon
	minimum?:       number
	maximum?:       number
	target?:        number
	area?:          bool
	progress_width?: number & >0
	thresholds?: [...#Threshold]
})

#KPIVisualPresentation: close({
	display_units?: "auto" | "none" | "thousands" | "millions" | "billions" | "trillions"
	note?:       string
	tone?:       "neutral" | "ink" | "success" | "warning" | "danger"
	thresholds?: [...#Threshold]
	conditional_formatting?: [...#ConditionalFormat]
})

#KPIValueBinding: close({
	dataset!: string & !=""
	field!:   string & !=""
	reducer?: "first" | "last" | "minimum" | "maximum" | "mean" | "median"
	label?:   string
})

#KPITrendBinding: close({
	dataset!:  string & !=""
	category!: string & !=""
	value!:    string & !=""
})

#KPIQualitativeRange: close({
	minimum?: number
	maximum?: number
	label!:   string & !=""
	tone!:    "neutral" | "ink" | "success" | "warning" | "danger"
})

#KPIConfiguration: close({
	mode?:                "compact" | "bullet" | "progress"
	comparison?:          #KPIValueBinding
	goal?:                #KPIValueBinding
	trend?:               #KPITrendBinding
	delta?:               "absolute" | "relative"
	favorable_direction?: "increase" | "decrease" | "neutral"
	missing_comparison?:  "show_unavailable" | "hide"
	ranges?:              [...#KPIQualitativeRange]
})

#CartesianVisual: close({
	#VisualCommon
	type!:         "line" | "area" | "bar" | "column" | "heatmap" | "candlestick" | "boxplot" | "combo" | "waterfall" | "histogram"
	query!:        #VisualQuery
	presentation?: #CartesianPresentation
})

#PointVisual: close({
	#VisualCommon
	type!:  "scatter"
	query!: #VisualQuery
	point!: close({
		identity!: [string & !="", ...string & !=""]
		x!:         string & !=""
		y!:         string & !=""
		size?:      string & !=""
		color?:     string & !=""
		series?:    string & !=""
		label?:     string & !=""
		tooltip?:   [...string & !=""]
		color_scale?: close({
			kind?:    "categorical" | "quantitative"
			minimum?: number
			maximum?: number
			scheme?:  string & !=""
		})
		size_scale?: close({
			minimum?:        number
			maximum?:        number
			minimum_pixels?: number & >0
			maximum_pixels?: number & >0
		})
		overplot?: close({
			strategy?:        "show_all" | "opacity"
			opacity?:         number & >=0 & <=1
			large_mode?:      "automatic" | "always" | "never"
			large_threshold?: int & >0
		})
		brush?: [..."rectangle" | "lasso"]
	})
	presentation?: close({
		#PresentationCommon
		axes?:              [...#CartesianAxis]
		reference_lines?:   [...#ReferenceLine]
		reference_bands?:   [...#ReferenceBand]
		event_annotations?: [...#EventAnnotation]
	})
})

#ProportionalVisual: close({
	#VisualCommon
	type!:         "pie" | "donut" | "funnel"
	query!:        #VisualQuery
	presentation?: #ProportionalPresentation
})

#HierarchyVisual: close({
	#VisualCommon
	type!:         "treemap" | "sankey" | "graph" | "tree" | "sunburst"
	query!:        #VisualQuery
	presentation?: #HierarchyPresentation
})

#PolarVisual: close({
	#VisualCommon
	type!:         "gauge" | "radar"
	query!:        #VisualQuery
	presentation?: #PolarPresentation
})

#GeographicVisual: close({
	#VisualCommon
	type!: "map"
	query!: #VisualQuery
	presentation?: close({
		#PresentationCommon
	})
	geo!: close({
		basemap?:       #Identifier | "blank"
		theme?:         "auto" | "light" | "dark"
		label_density?: "hidden" | "normal" | "dense"
		camera?: close({
			mode?:     "fit_data" | "fixed" | "preserve"
			center?:   [number, number]
			zoom?:     number & >=0 & <=24
			padding?:  int & >=0
			min_zoom?: number & >=0 & <=24
			max_zoom?: number & >=0 & <=24
		})
		controls?: close({zoom?: bool, reset?: bool, compass?: bool})
		layers!: [#GeographicLayer, ...#GeographicLayer]
	})
})

#GeographicLayerCommon: {
	id!:         #Identifier
	value?:      #Identifier
	category?:   #Identifier
	label?:      #Identifier
	tooltip?:    [...#Identifier]
	position?:   "below_labels" | "above_labels"
	visibility?: close({min_zoom?: number & >=0 & <=24, max_zoom?: number & >=0 & <=24})
	color?: close({
		kind?:            "sequential" | "diverging" | "categorical"
		palette?:         #Identifier
		reverse?:         bool
		domain_minimum?:  number
		domain_midpoint?: number
		domain_maximum?:  number
		null_color?:      string
	})
	stroke?:  close({color?: string, width?: number & >=0, opacity?: number & >=0 & <=1})
	opacity?: number & >=0 & <=1
}

#GeographicLayer: close({
	#GeographicLayerCommon
	kind!:           "choropleth"
	geometry_asset!: #Identifier
	join!:           #Identifier
}) | close({
	#GeographicLayerCommon
	kind!:      "point"
	latitude!:  #Identifier
	longitude!: #Identifier
	size?: close({minimum_radius?: number & >=0, maximum_radius?: number & >=0, domain_minimum?: number, domain_maximum?: number})
	cluster?: close({enabled?: bool, radius?: int & >0, max_zoom?: int & >=0 & <=24, minimum_points?: int & >=2, show_count?: bool})
}) | close({
	#GeographicLayerCommon
	kind!:      "heat" | "density"
	latitude!:  #Identifier
	longitude!: #Identifier
	heat?: close({radius?: number & >0, intensity?: number & >0})
}) | close({
	#GeographicLayerCommon
	kind!:           "reference"
	geometry_asset!: #Identifier
}) | close({
	#GeographicLayerCommon
	kind!:      "path"
	latitude!:  #Identifier
	longitude!: #Identifier
	path!:      #Identifier
	order!:     #Identifier
	line?: close({width?: number & >0, curvature?: number & >=0 & <=1})
})

#KPIVisual: close({
	#VisualCommon
	type!:    "kpi"
	query!:   #VisualQuery
	kpi?:     #KPIConfiguration
	presentation?: #KPIVisualPresentation
})

#TabularVisualCommon: {
	#VisualCommon
	title!:       string
	cardinality?: "bounded" | "exact"
	default_sort?: close({
		key?:       string
		direction?: "asc" | "desc" | string
	})
	presentation?: close({
		density?: "compact" | "comfortable" | "spacious" | string
		zebra?:   bool
		grid?:    "none" | "rows" | "columns" | "full" | string
	})
	columns?: [...#TableColumn]
	measure_formatting?: {
		[string]: [...#TableFormattingRule]
	}
	conditional_formatting?: [...#ConditionalFormat]
}

#DataTableVisual: close({
	#TabularVisualCommon
	type!:  "table"
	query!: #TableQuery
})

#MatrixVisual: close({
	#TabularVisualCommon
	type!:  "matrix"
	query!: #TableQuery
})

#PivotVisual: close({
	#TabularVisualCommon
	type!:  "pivot"
	query!: #TableQuery
})

#VisualQuery: close({
	table?:      #Identifier
	dimensions?: #FieldRefs
	series?:     #FieldRefObject
	measures?:   #MeasureRefs
	time?: close({
		field?: #FieldRef | #Identifier
		grain?: string
		alias?: #Identifier
	})
	sort?: [...#Sort]
	limit?: int
})

#FieldRefs: [...#FieldRefValue] | close({
	[#Identifier]: (#FieldRef | #Identifier) | close({field: #FieldRef | #Identifier})
})

#MeasureRefs: [...#FieldRefValue] | close({
	[#Identifier]: null | close({
		measure!: #Identifier
	})
})

#FieldRefValue: #FieldRef | #Identifier | #FieldRefObject

#FieldRefObject: close({
	field!: #FieldRef | #Identifier
	alias!: #Identifier
})

#Sort: close({
	field?:     string
	direction?: "asc" | "desc" | string
	expr?:      string
})

#Interaction: close({
	point_selection?: #SelectionInteraction
	row_selection?:   #SelectionInteraction
	spatial_selection?: #SpatialSelectionInteraction
})

#SelectionInteraction: close({
	toggle?: bool
	mappings?: [...close({
		field!: #FieldRef | #Identifier
		fact?:  #Identifier
		grain?: "day" | "week" | "month" | "quarter" | "year"
		value!: string
		label?: string
	})]
	targets?:           [...#Identifier]
	highlight_targets?: [...#Identifier]
	none_targets?:      [...#Identifier]
})

#SpatialSelectionInteraction: close({
	gestures!: [...("box" | "lasso" | "radius")]
	latitude!:  #SpatialSelectionMapping
	longitude!: #SpatialSelectionMapping
	targets!:           [...#Identifier]
	highlight_targets?: [...#Identifier]
	none_targets?:      [...#Identifier]
})

#SpatialSelectionMapping: close({
	source!: #Identifier
	field!:  #FieldRef | #Identifier
	fact?:   #Identifier
})

#TableQuery: close({
	table?: #Identifier
	fields?: [...#FieldRef]
	columns?:  #FieldRefs
	rows?:     #FieldRefs
	measures?: #MeasureRefs
})

#TableColumn: close({
	key!:          string
	label?:        string
	width?:        int
	format?:       "text" | "integer" | "decimal" | "currency" | "days" | string
	role?:         string
	align?:        string
	group?:        string
	measure?:      string
	column_value?: string
	formatting?: [...#TableFormattingRule]
})

#TableFormattingRule: close({
	kind!: "badge" | "text_color" | "background_scale" | "data_bar"
	values?: {[string]: string}
	min?:        number
	max?:        number
	color?:      string
	background?: string
	low_color?:  string
	high_color?: string
})

#Page: close({
	id!:          #ObjectID
	title!:       string
	description?: string
	canvas?: close({
		width?:  int
		height?: int
	})
	grid?: close({
		columns?:    int
		row_height?: int
		gap?:        int
		padding?:    int
	})
	filter_bindings?: close({[#Identifier]: #FilterBinding})
	components!: [...#PageComponent]
})

#PageComponent: #VisualComponent | #SlicerComponent | #HeaderComponent

#PageComponentCommon: {
	id!:          #ObjectID
	description?: string
	placement!: close({
		col!:      int
		row!:      int
		col_span!: int
		row_span!: int
	})
	eyebrow?:  string
	title?:    string
	subtitle?: string
	badges?: [...string]
}

#VisualComponent: close({
	#PageComponentCommon
	kind!:   "visual"
	visual!: #Identifier
})

#SlicerComponent: close({
	#PageComponentCommon
	kind!:    "slicer"
	binding!: #FilterBindingRef
	presentation?: close({
		style?:         "dropdown" | "list" | "buttons" | "input" | "numeric_range" | "date_range" | "relative_period"
		search?:        bool
		select_all?:    bool
		show_counts?:   bool
		show_summary?:  bool
		compact?:        bool
		title?:          string
		description?:    string
		aria_label?:     string
	})
})

#HeaderComponent: close({
	#PageComponentCommon
	kind!: "header"
})
