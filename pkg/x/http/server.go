package http

import (
	"net/http"
	"reflect"
	"slices"

	"github.com/omalloc/tavern/contrib/log"
)

func PrintRoutes(mux *http.ServeMux) {
	var routes []string

	var walk func(reflect.Value)
	walk = func(node reflect.Value) {
		if !node.IsValid() {
			return
		}

		patternField := node.FieldByName("pattern")
		if patternField.IsValid() && !patternField.IsNil() {
			pat := patternField.Elem()
			strField := pat.FieldByName("str")
			if strField.IsValid() {
				routes = append(routes, strField.String())
			}
		}

		childrenField := node.FieldByName("children")
		if childrenField.IsValid() {
			sField := childrenField.FieldByName("s")
			if sField.IsValid() && sField.Kind() == reflect.Slice {
				for i := 0; i < sField.Len(); i++ {
					entry := sField.Index(i)
					val := entry.FieldByName("value")
					walk(val.Elem())
				}
			}
			mField := childrenField.FieldByName("m")
			if mField.IsValid() && mField.Kind() == reflect.Map {
				keys := mField.MapKeys()
				for _, k := range keys {
					val := mField.MapIndex(k)
					walk(val.Elem())
				}
			}
		}

		multiChild := node.FieldByName("multiChild")
		if multiChild.IsValid() && !multiChild.IsNil() {
			walk(multiChild.Elem())
		}

		emptyChild := node.FieldByName("emptyChild")
		if emptyChild.IsValid() && !emptyChild.IsNil() {
			walk(emptyChild.Elem())
		}
	}

	val := reflect.ValueOf(mux).Elem()
	treeField := val.FieldByName("tree")
	walk(treeField)

	// Also check mux121 for older Go versions or compatibility
	mux121Field := val.FieldByName("mux121")
	if mux121Field.IsValid() {
		mField := mux121Field.FieldByName("m")
		if mField.IsValid() && mField.Kind() == reflect.Map {
			keys := mField.MapKeys()
			for _, k := range keys {
				routes = append(routes, k.String())
			}
		}
	}

	// Deduplicate and sort
	slices.Sort(routes)
	routes = slices.Compact(routes)

	for _, r := range routes {
		log.Infof("router handler %s", r)
	}
}
