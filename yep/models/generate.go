// Copyright 2016 NDP Systèmes. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package models

import (
	"fmt"
	"path"
	"reflect"
	"strings"
	"text/template"

	"github.com/npiganeau/yep/yep/tools/generate"
)

// A fieldData describes a field in a RecordSet
type fieldData struct {
	Name     string
	Type     string
	TypeIsRS bool
}

// A methodData describes a method in a RecordSet
type methodData struct {
	Name           string
	Doc            string
	Params         string
	ParamsWithType string
	Returns        string
	ReturnsIsRS    bool
}

// A modelData describes a RecordSet model
type modelData struct {
	Name    string
	Deps    []string
	Fields  []fieldData
	Methods []methodData
}

// specificMethods are generated according to specific templates and thus
// must not be wrapped automatically.
var specificMethods = map[string]bool{
	"Create": true,
	"First":  true,
	"All":    true,
}

// GeneratePool generates source code files inside the given directory for all models.
//
// GeneratePool works by reflection and cannot infer the names of the parameters of
// each methods. That's why we need to pass it a map with the method's AST data. Such
// a map can be generated by generate.GetMethodsASTData()
func GeneratePool(dir string, astData map[generate.MethodRef]generate.MethodASTData) {
	// We need to simulate bootstrapping to get embedded and mixed in fields
	createModelLinks()
	inflateMixIns()
	inflateEmbeddings()
	generateMethodsDoc()
	// Now we can generate pool for each model
	for modelName, mi := range modelRegistry.registryByName {
		fileName := fmt.Sprintf("%s.go", strings.ToLower(modelName))
		generateModelPoolFile(mi, path.Join(dir, fileName), astData)
	}
}

// generateModelPoolFile generates the file with the source code of the
// pool object for the given modelInfo.
func generateModelPoolFile(mi *modelInfo, fileName string, astData map[generate.MethodRef]generate.MethodASTData) {
	// Generate model data
	deps := map[string]bool{
		generate.PoolPath: true,
	}
	mData := modelData{
		Name: mi.name,
		Deps: []string{generate.ModelsPath},
	}
	// Add fields
	addFieldsToModelData(&mData, mi, &deps)
	// Add methods
	addMethodsToModelData(&mData, mi, astData, &deps)
	// Create file
	generate.CreateFileFromTemplate(fileName, defsFileTemplate, mData)
	log.Info("Generated pool source file for model", "model", mi.name, "fileName", fileName)
}

// sanitizeFieldType returns the sanitizes name of the type
// and a bool value that is true if the type is a RecordSet
func sanitizedFieldType(mi *modelInfo, typ reflect.Type) (string, bool) {
	var isRC bool
	typStr := typ.String()
	if typ == reflect.TypeOf(RecordCollection{}) {
		isRC = true
		typStr = fmt.Sprintf("%sSet", mi.name)
	}
	return strings.Replace(typStr, "pool.", "", 1), isRC
}

// addDependency adds the given type's path as dependency to the given modelData
// if not already imported. deps is the map of already imported paths and is updated
// by this function.
func addDependency(data *modelData, typ reflect.Type, deps *map[string]bool) {
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	fDep := typ.PkgPath()
	if fDep != "" && !(*deps)[fDep] {
		data.Deps = append(data.Deps, fDep)
	}
	(*deps)[fDep] = true
}

// addFieldsToModelData adds the fields of the given modelInfo to the given modelData.
// deps is the map of already imported paths and is updated by this function.
func addFieldsToModelData(mData *modelData, mi *modelInfo, deps *map[string]bool) {
	for fieldName, fi := range mi.fields.registryByName {
		var (
			typStr  string
			typIsRS bool
		)
		if fi.isRelationField() {
			typStr = fmt.Sprintf("%sSet", fi.relatedModelName)
			typIsRS = true
		} else {
			typStr, _ = sanitizedFieldType(mi, fi.structField.Type)
		}
		mData.Fields = append(mData.Fields, fieldData{
			Name:     fieldName,
			Type:     typStr,
			TypeIsRS: typIsRS,
		})
		// Add dependency for this field, if needed and not already imported
		addDependency(mData, fi.structField.Type, deps)
	}
}

// addMethodsToModelData adds the methods of the given modelInfo to the given modelData.
// astData is a map of MethodASTData from which to get doc strings and params names.
// deps is the map of already imported paths and is updated by this function.
func addMethodsToModelData(mData *modelData, mi *modelInfo, astData map[generate.MethodRef]generate.MethodASTData, deps *map[string]bool) {
	for methodName, methInfo := range mi.methods.registry {
		if specificMethods[methodName] {
			continue
		}

		ref := generate.MethodRef{Model: mi.name, Method: methodName}
		dParams, ok := astData[ref]
		if !ok {
			// Check if we have the method in mixins
			allMixIns := append(modelRegistry.commonMixins, mi.mixins...)
			var mixInMethFound bool
			for i := len(allMixIns) - 1; i >= 0; i-- {
				mixInRef := generate.MethodRef{Model: allMixIns[i].name, Method: methodName}
				dParams, ok = astData[mixInRef]
				if ok {
					mixInMethFound = true
					break
				}
			}
			// Else we suppose it's a method generated in 'yep/models' and doesn't have a model set
			if !mixInMethFound {
				newRef := generate.MethodRef{Model: "", Method: methodName}
				dParams = astData[newRef]
			}
		}
		methType := methInfo.methodType
		params := make([]string, methType.NumIn()-1)
		paramsType := make([]string, methType.NumIn()-1)
		for i := 0; i < methType.NumIn()-1; i++ {
			var (
				varArgType, pType string
				isRC              bool
			)
			if methType.IsVariadic() && i == methType.NumIn()-2 {
				varArgType, isRC = sanitizedFieldType(mi, methType.In(i+1).Elem())
				pType = fmt.Sprintf("...%s", varArgType)
			} else {
				pType, isRC = sanitizedFieldType(mi, methType.In(i+1))
			}
			paramsType[i] = fmt.Sprintf("%s %s", dParams.Params[i], pType)
			if isRC {
				params[i] = fmt.Sprintf("%s.RecordCollection", dParams.Params[i])
			} else {
				params[i] = dParams.Params[i]
			}
			addDependency(mData, methType.In(i+1), deps)
		}

		var (
			returns     string
			returnsIsRS bool
		)
		if methType.NumOut() > 0 {
			returns, returnsIsRS = sanitizedFieldType(mi, methType.Out(0))
			addDependency(mData, methType.Out(0), deps)
		}
		methData := methodData{
			Name:           methodName,
			Doc:            methInfo.doc,
			Params:         strings.Join(params, ", "),
			ParamsWithType: strings.Join(paramsType, ", "),
			Returns:        returns,
			ReturnsIsRS:    returnsIsRS,
		}
		mData.Methods = append(mData.Methods, methData)
	}
}

var defsFileTemplate = template.Must(template.New("").Parse(`
// This file is autogenerated by yep-generate
// DO NOT MODIFY THIS FILE - ANY CHANGES WILL BE OVERWRITTEN

package pool

import (
{{ range .Deps }} 	"{{ . }}"
{{ end }}
)

const (
{{ range .Fields }}	{{ $.Name }}_{{ .Name }} models.FieldName = "{{ .Name }}"
{{ end }}
	Model{{ $.Name }} models.ModelName = "{{ $.Name }}"
)

// {{ .Name }} is an autogenerated struct type to handle {{ .Name }} data.
type {{ .Name }} struct {
{{ range .Fields }}	{{ .Name }} {{ .Type }}
{{ end }}
}

// {{ .Name }}Set is an autogenerated type to handle {{ .Name }} objects.
type {{ .Name }}Set struct {
	models.RecordCollection
}

// New{{ .Name }}Set returns a new {{ .Name }}Set instance in the given Environment
func New{{ .Name }}Set(env models.Environment) {{ .Name }}Set {
	return {{ .Name }}Set{
		RecordCollection: env.Pool("{{ .Name }}"),
	}
}

var _ models.RecordSet = {{ .Name }}Set{}

// First returns a copy of the first Record of this RecordSet.
// It returns an empty {{ .Name }} if the RecordSet is empty.
func (s {{ .Name }}Set) First() {{ .Name }} {
	var res {{ .Name }}
	s.RecordCollection.First(&res)
	return res
}

// All Returns a copy of all records of the RecordCollection.
// It returns an empty slice if the RecordSet is empty.
func (s {{ .Name }}Set) All() []{{ .Name }} {
	var ptrSlice []*{{ .Name }}
	s.RecordCollection.All(&ptrSlice)
	res := make([]{{ .Name }}, len(ptrSlice))
	for i, ps := range ptrSlice {
		res[i] = *ps
	}
	return res
}

// Records returns a slice with all the records of this RecordSet, as singleton
// RecordSets
func (s {{ .Name }}Set) Records() []{{ .Name }}Set {
	res := make([]{{ .Name }}Set, len(s.RecordCollection.Records()))
	for i, rec := range s.RecordCollection.Records() {
		res[i] = {{ .Name }}Set{
			RecordCollection: rec,
		}
	}
	return res
}

// Create inserts a record in the database from the given {{ .Name }} data.
// Returns the created {{ .Name }}Set.
func (s {{ $.Name }}Set) Create(data *{{ .Name }}) {{ .Name }}Set {
	return {{ .Name }}Set{
		RecordCollection: s.Call("Create", data).(models.RecordCollection),
	}
}

{{ range .Fields }}
// {{ .Name }} is a getter for the value of the "{{ .Name }}" field of the first
// record in this RecordSet. It returns the Go zero value if the RecordSet is empty.
func (s {{ $.Name }}Set) {{ .Name }}() {{ .Type }} {
{{ if .TypeIsRS }}	return {{ .Type }}{
		RecordCollection: s.RecordCollection.Get("{{ .Name }}").(models.RecordCollection),
	}{{ else -}}
	return s.RecordCollection.Get("{{ .Name }}").({{ .Type }}) {{ end }}
}

// Set{{ .Name }} is a setter for the value of the "{{ .Name }}" field of this
// RecordSet. All Records of this RecordSet will be updated. Each call to this
// method makes an update query in the database.
//
// Set{{ .Name }} panics if the RecordSet is empty.
func (s {{ $.Name }}Set) Set{{ .Name }}(value {{ .Type }}) {
	s.RecordCollection.Set("{{ .Name }}", value)
}
{{ end }}

{{ range .Methods }}
{{ .Doc }}
func (s {{ $.Name }}Set) {{ .Name }}({{ .ParamsWithType }}) {{ .Returns }} {
{{ if eq .Returns "" -}}
	s.Call("{{ .Name }}", {{ .Params}})
{{- else }}{{ if .ReturnsIsRS -}}
	return {{ .Returns }}{
		RecordCollection: s.Call("{{ .Name }}", {{ .Params}}).(models.RecordCollection),
	}
{{- else -}}
	return s.Call("{{ .Name }}", {{ .Params}}).({{ .Returns }})
{{- end -}}
{{- end }}
}

{{ end }}
`))
