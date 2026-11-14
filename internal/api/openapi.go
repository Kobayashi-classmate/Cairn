// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var pathParam = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// OpenAPI builds an OpenAPI 3.1 document from the route registry. Request
// and response schemas are derived from the Go types given with Body and
// Returns; permissions and step-up appear as x-cairn-* extensions.
func (rt *Router) OpenAPI(title, version string) map[string]any {
	gen := &schemaGen{defs: map[string]any{}, names: map[reflect.Type]string{}}
	paths := map[string]map[string]any{}
	var tags []any
	seenTag := map[string]bool{}
	for _, r := range rt.routes {
		tag := r.Tag
		if tag == "" {
			tag = "other"
		}
		if !seenTag[tag] {
			seenTag[tag] = true
			tags = append(tags, map[string]any{"name": tag})
		}
		op := map[string]any{
			"operationId":     operationID(r),
			"summary":         r.Summary,
			"tags":            []string{tag},
			"x-cairn-step-up": r.StepUp,
		}
		if r.Public {
			op["security"] = []any{}
		} else {
			op["x-cairn-permission"] = string(r.Permission)
			op["security"] = []any{map[string]any{"session": []any{}}, map[string]any{"bearer": []any{}}}
		}
		if r.Module != "" {
			op["x-cairn-module"] = r.Module
		}
		var params []any
		for _, m := range pathParam.FindAllStringSubmatch(r.Path, -1) {
			params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
		}
		if params != nil {
			op["parameters"] = params
		}
		if r.Request != nil {
			op["requestBody"] = map[string]any{"required": true, "content": jsonContent(gen.schema(r.Request))}
		}
		responses := map[string]any{"default": map[string]any{"$ref": "#/components/responses/Error"}}
		for status, t := range r.Responses {
			resp := map[string]any{"description": http.StatusText(status)}
			if t != nil {
				resp["content"] = jsonContent(gen.schema(t))
			}
			responses[strconv.Itoa(status)] = resp
		}
		if len(r.Responses) == 0 {
			responses["200"] = map[string]any{"description": "OK"}
		}
		op["responses"] = responses
		if paths[r.Path] == nil {
			paths[r.Path] = map[string]any{}
		}
		paths[r.Path][strings.ToLower(r.Method)] = op
	}
	errorSchema := map[string]any{
		"type": "object", "required": []string{"error"},
		"properties": map[string]any{"error": map[string]any{
			"type": "object", "required": []string{"code"},
			"properties": map[string]any{
				"code":   map[string]any{"type": "string", "description": "Stable error code; translated by clients"},
				"params": map[string]any{"type": "object"},
			},
		}},
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": title, "version": version},
		"tags":    tags, // in registration order, which is module order
		"paths":   paths,
		"components": map[string]any{
			"schemas": gen.defs,
			"responses": map[string]any{"Error": map[string]any{
				"description": "Error with a code and parameters",
				"content":     jsonContent(errorSchema),
			}},
			"securitySchemes": map[string]any{
				"session": map[string]any{"type": "apiKey", "in": "cookie", "name": "__Host-cairn_session"},
				"bearer":  map[string]any{"type": "http", "scheme": "bearer", "description": "API token (cairn_...)"},
			},
		},
	}
}

// ServeOpenAPI returns a handler serving the document, built once on first
// use (after every module has registered its routes).
func (rt *Router) ServeOpenAPI(title, version string) HandlerFunc {
	var once sync.Once
	var doc map[string]any
	return func(w http.ResponseWriter, _ *http.Request) error {
		once.Do(func() { doc = rt.OpenAPI(title, version) })
		return WriteJSON(w, http.StatusOK, doc)
	}
}

func jsonContent(schema map[string]any) map[string]any {
	return map[string]any{"application/json": map[string]any{"schema": schema}}
}

func operationID(r Route) string {
	p := pathParam.ReplaceAllString(strings.TrimPrefix(r.Path, "/api/v1/"), "by_$1")
	p = strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(p)
	return strings.ToLower(r.Method) + "_" + p
}

// schemaGen reflects Go types into JSON Schemas, registering named structs
// once under components.schemas.
type schemaGen struct {
	defs  map[string]any
	names map[reflect.Type]string
}

var timeType = reflect.TypeOf(time.Time{})

func (g *schemaGen) schema(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.Pointer:
		return map[string]any{"anyOf": []any{g.schema(t.Elem()), map[string]any{"type": "null"}}}
	case reflect.Struct:
		if t == timeType {
			return map[string]any{"type": "string", "format": "date-time"}
		}
		if t.Name() == "" {
			return g.object(t)
		}
		name := g.name(t)
		if _, done := g.defs[name]; !done {
			g.defs[name] = map[string]any{} // placeholder breaks recursion
			g.defs[name] = g.object(t)
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{"type": "array", "items": g.schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": g.schema(t.Elem())}
	case reflect.Interface:
		return map[string]any{}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	}
	return map[string]any{}
}

// name is "<package>.<Type>", unique per type.
func (g *schemaGen) name(t reflect.Type) string {
	if n, ok := g.names[t]; ok {
		return n
	}
	n := path.Base(t.PkgPath()) + "." + t.Name()
	for i := 2; ; i++ {
		taken := false
		for _, other := range g.names {
			if other == n {
				taken = true
				break
			}
		}
		if !taken {
			break
		}
		n = path.Base(t.PkgPath()) + "." + t.Name() + strconv.Itoa(i)
	}
	g.names[t] = n
	return n
}

// object describes a struct, honoring json tags and flattening embedded
// structs the way encoding/json does.
func (g *schemaGen) object(t reflect.Type) map[string]any {
	props := map[string]any{}
	var required []string
	g.fields(t, props, &required)
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func (g *schemaGen) fields(t reflect.Type, props map[string]any, required *[]string) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				g.fields(ft, props, required)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		props[name] = g.schema(f.Type)
		if !strings.Contains(opts, "omitempty") && f.Type.Kind() != reflect.Pointer {
			*required = append(*required, name)
		}
	}
}
