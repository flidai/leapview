package main

import (
	"encoding/json"
	"os"
	"text/template"
)

type input struct {
	Template string                 `json:"template"`
	Data     map[string]interface{} `json:"data"`
}

func main() {
	var in input
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		panic(err)
	}
	functions := template.FuncMap{"json": func(value interface{}) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		return string(encoded)
	}}
	t, err := template.New("docker-inspect").Option("missingkey=error").Funcs(functions).Parse(in.Template)
	if err != nil {
		panic(err)
	}
	if err = t.Execute(os.Stdout, in.Data); err != nil {
		panic(err)
	}
}
