package cli

import "github.com/spf13/cobra"

const (
	documentationEffectAnnotation       = "leapview.dev/effect"
	documentationConfirmationAnnotation = "leapview.dev/confirmation"
	documentationHelpGroupAnnotation    = "leapview.dev/help-group"
)

type commandSafety struct {
	effect       string
	confirmation string
}

func annotateCommandDocumentation(root *cobra.Command) {
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		if safety, ok := documentedCommandSafety[command.CommandPath()]; ok {
			if command.Annotations == nil {
				command.Annotations = map[string]string{}
			}
			command.Annotations[documentationEffectAnnotation] = safety.effect
			command.Annotations[documentationConfirmationAnnotation] = safety.confirmation
		}
		for _, child := range command.Commands() {
			visit(child)
		}
	}
	visit(root)
}

var documentedCommandSafety = map[string]commandSafety{
	"leapview":                                 {effect: "local-write", confirmation: "never"},
	"leapview admin backup":                    {effect: "local-write", confirmation: "never"},
	"leapview admin initialize":                {effect: "write", confirmation: "never"},
	"leapview admin maintenance":               {effect: "destructive", confirmation: "conditional"},
	"leapview admin restore":                   {effect: "destructive", confirmation: "required"},
	"leapview admin storage cleanup":           {effect: "destructive", confirmation: "conditional"},
	"leapview admin delivery pool bootstrap":   {effect: "write", confirmation: "required"},
	"leapview admin delivery pool qualify":     {effect: "write", confirmation: "required"},
	"leapview admin delivery audit":            {effect: "read", confirmation: "never"},
	"leapview admin delivery repair":           {effect: "destructive", confirmation: "conditional"},
	"leapview agent ask":                       {effect: "write", confirmation: "never"},
	"leapview agent conversations":             {effect: "read", confirmation: "never"},
	"leapview agent tools":                     {effect: "read", confirmation: "never"},
	"leapview api call":                        {effect: "dynamic", confirmation: "never"},
	"leapview api describe":                    {effect: "read", confirmation: "never"},
	"leapview api list":                        {effect: "read", confirmation: "never"},
	"leapview config validate":                 {effect: "read", confirmation: "never"},
	"leapview dashboards describe":             {effect: "read", confirmation: "never"},
	"leapview dashboards export":               {effect: "local-write", confirmation: "never"},
	"leapview dashboards filter":               {effect: "read", confirmation: "never"},
	"leapview dashboards filter-options":       {effect: "read", confirmation: "never"},
	"leapview dashboards list":                 {effect: "read", confirmation: "never"},
	"leapview dashboards page":                 {effect: "read", confirmation: "never"},
	"leapview dashboards query-page":           {effect: "read", confirmation: "never"},
	"leapview dashboards visual":               {effect: "read", confirmation: "never"},
	"leapview dashboards visual-data":          {effect: "read", confirmation: "never"},
	"leapview data plan":                       {effect: "read", confirmation: "never"},
	"leapview data revisions current":          {effect: "read", confirmation: "never"},
	"leapview data revisions list":             {effect: "read", confirmation: "never"},
	"leapview data sync":                       {effect: "write", confirmation: "never"},
	"leapview build":                           {effect: "write", confirmation: "conditional"},
	"leapview deploy":                          {effect: "write", confirmation: "conditional"},
	"leapview dev":                             {effect: "write", confirmation: "never"},
	"leapview evaluate":                        {effect: "local-write", confirmation: "never"},
	"leapview evaluate first-login":            {effect: "destructive", confirmation: "never"},
	"leapview healthcheck":                     {effect: "read", confirmation: "never"},
	"leapview login":                           {effect: "local-write", confirmation: "never"},
	"leapview logout":                          {effect: "destructive", confirmation: "never"},
	"leapview plan":                            {effect: "write", confirmation: "conditional"},
	"leapview publish":                         {effect: "write", confirmation: "conditional"},
	"leapview rollback":                        {effect: "write", confirmation: "conditional"},
	"leapview schema export":                   {effect: "local-write", confirmation: "never"},
	"leapview search":                          {effect: "read", confirmation: "never"},
	"leapview semantic-models dataset":         {effect: "read", confirmation: "never"},
	"leapview semantic-models datasets":        {effect: "read", confirmation: "never"},
	"leapview semantic-models describe":        {effect: "read", confirmation: "never"},
	"leapview semantic-models explain-preview": {effect: "read", confirmation: "never"},
	"leapview semantic-models explain-query":   {effect: "read", confirmation: "never"},
	"leapview semantic-models fields":          {effect: "read", confirmation: "never"},
	"leapview semantic-models list":            {effect: "read", confirmation: "never"},
	"leapview semantic-models preview":         {effect: "read", confirmation: "never"},
	"leapview semantic-models query":           {effect: "read", confirmation: "never"},
	"leapview semantic-model ossie import":     {effect: "read", confirmation: "never"},
	"leapview semantic-model ossie export":     {effect: "read", confirmation: "never"},
	"leapview serve":                           {effect: "local-write", confirmation: "never"},
	"leapview validate":                        {effect: "read", confirmation: "never"},
	"leapview version":                         {effect: "read", confirmation: "never"},
}
